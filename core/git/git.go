package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// TODO: Make this configurable
	_gitTimeout = 10 * time.Minute
)

// Interface defines the interface to execute git commands
type Interface interface {
	Checkout(ctx context.Context, ref string, options ...string) error
	Diff(ctx context.Context, baseRef, targetRef string, options ...string) ([]byte, error)
	Fetch(ctx context.Context, remote, ref string, options ...string) error
	Clone(ctx context.Context, target, destination string, options ...string) error
	ApplyPatch(ctx context.Context, patch []byte) error
	RevParse(ctx context.Context, ref string) (string, error)
	IsAncestor(ctx context.Context, ancestorRef, descendantRef string) (bool, error)
	Commit(ctx context.Context, message string, options ...string) error
	SubmoduleUpdate(ctx context.Context) error
	FileHashes(ctx context.Context, ref string) (map[string][]byte, error)
}

type impl struct {
	directory string
	runner    commandRunner
}

// New creates new Git interface implementation
func New(directory string) Interface {
	return &impl{
		directory: directory,
		runner:    &osExecRunner{},
	}
}

// Checkout checks out a specific reference in the repository.
func (c *impl) Checkout(ctx context.Context, ref string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"checkout", ref}, options...)
	return c.runner.run(ctx, c.directory, "git", args...)
}

// Fetch runs git fetch for a remote ref.
func (c *impl) Fetch(ctx context.Context, remote, ref string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"fetch", remote, ref}, options...)
	return c.runner.run(ctx, c.directory, "git", args...)
}

// Clone clones the target repository to the destination.
// The target repository can be either a remote repository or a local repository.
func (c *impl) Clone(ctx context.Context, target, destination string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"clone", target, destination}, options...)
	return c.runner.run(ctx, c.directory, "git", args...)
}

// Diff returns the diff between two references.
func (c *impl) Diff(ctx context.Context, baseRef, targetRef string, options ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"diff", baseRef, targetRef}, options...)
	return c.runner.output(ctx, c.directory, "git", args...)
}

// ApplyPatch applies a patch to the repository.
func (c *impl) ApplyPatch(ctx context.Context, patch []byte) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	return c.runner.runWithStdin(ctx, c.directory, "git", patch, "apply", "--3way", "--whitespace", "nowarn", "--index", "-")
}

// RevParse returns the revision hash of a reference.
func (c *impl) RevParse(ctx context.Context, ref string) (string, error) {
	args := []string{"rev-parse", ref}
	out, err := c.runner.output(ctx, c.directory, "git", args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Log returns up to limit commit SHAs reachable from ref, most recent first.
func (c *impl) IsAncestor(ctx context.Context, ancestorRef, descendantRef string) (bool, error) {
	_, err := c.runner.output(ctx, c.directory, "git", "merge-base", "--is-ancestor", ancestorRef, descendantRef)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return false, nil
			}
			// an exit code other than 1 indicates an error
			return false, fmt.Errorf("check if ref %s is ancestor of %s: %w", ancestorRef, descendantRef, err)
		}
	}
	return true, nil
}

// Commit commits the changes to the repository.
func (c *impl) Commit(ctx context.Context, message string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"commit", "-am", message}, options...)
	return c.runner.run(ctx, c.directory, "git", args...)
}

// SubmoduleUpdate updates the submodules in the repository.
func (c *impl) SubmoduleUpdate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"submodule", "update", "--init", "--recursive"}
	return c.runner.run(ctx, c.directory, "git", args...)
}

// FileHashes gets a mapping of files to their hashes based on `git ls-tree --full-tree -r <ref>`.
func (c *impl) FileHashes(ctx context.Context, ref string) (map[string][]byte, error) {
	out, err := c.runner.output(ctx, c.directory, "git", "ls-tree", "--full-tree", "-r", ref)
	if err != nil {
		return nil, err
	}

	fileHashes := make(map[string][]byte)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	// git ls-tree --full-tree output format:
	// 100644 blob d236089b460070849370c1c813874ae9bea598a8 file1
	// 100644 blob 9bccabefa0c7a4d68b219e2b62d6d3cc5271cf44 file2
	// 100644 blob a14cddefd9112ee21e58f6c78ad224dc44a64297 file3
	for scanner.Scan() {
		line := scanner.Text()
		x := strings.Split(line, "\t")
		parts := strings.Fields(x[0]) // strings.Split is guaranteed to return an array of length 1
		if len(parts) < 3 || len(x) < 2 {
			log.Printf("skipping %q due to unexpected format\n", line)
			continue
		}
		hash, err := hex.DecodeString(parts[2])
		if err != nil {
			log.Printf("skipping %q due to parsing error %v\n", line, err)
			continue
		}
		fileHashes[x[1]] = hash
	}
	return fileHashes, scanner.Err()
}

// commandRunner abstracts command execution for testability.
type commandRunner interface {
	run(ctx context.Context, dir string, name string, args ...string) error
	output(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
	runWithStdin(ctx context.Context, dir string, name string, stdin []byte, args ...string) error
}

type osExecRunner struct{}

func (r *osExecRunner) run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if errors.Is(cmd.Err, exec.ErrDot) {
		cmd.Err = nil
	}
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *osExecRunner) output(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if errors.Is(cmd.Err, exec.ErrDot) {
		cmd.Err = nil
	}
	cmd.Dir = dir
	return cmd.Output()
}

func (r *osExecRunner) runWithStdin(ctx context.Context, dir string, name string, stdin []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if errors.Is(cmd.Err, exec.ErrDot) {
		cmd.Err = nil
	}
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
