// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package git

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/uber/tango/core/execcmd"
	"go.uber.org/zap"
)

const (
	// TODO: Make this configurable
	_gitTimeout = 10 * time.Minute
)

// ErrFatal is returned (wrapped) when a git command exits with a fatal (128)
// or usage (129) exit code -- e.g. an unreachable remote, failed
// authentication, an invalid repository, or a malformed invocation -- as
// opposed to a non-fatal, conditional exit code such as 1. Callers can check
// for it with errors.Is.
var ErrFatal = errors.New("git command failed fatally")

// ErrTimeout is returned (wrapped) when a git command does not complete
// within the configured timeout. Callers can check for it with errors.Is.
var ErrTimeout = errors.New("git command timed out")

// DiffEntry represents a single file change from git diff --name-status.
type DiffEntry struct {
	// Status is the single-character status code: "A", "D", "M", "R", etc.
	Status string
	// Path is the file path. For renames, this is the destination path.
	Path string
}

// Interface defines the interface to execute git commands
type Interface interface {
	Checkout(ctx context.Context, ref string, options ...string) error
	Diff(ctx context.Context, baseRef, targetRef string, options ...string) ([]byte, error)
	DiffWithStatus(ctx context.Context, baseRef, targetRef string) ([]DiffEntry, error)
	Fetch(ctx context.Context, remote, ref string, options ...string) error
	Clone(ctx context.Context, target, destination string, options ...string) error
	ApplyPatch(ctx context.Context, patch []byte) error
	RevParse(ctx context.Context, ref string) (string, error)
	IsAncestor(ctx context.Context, ancestorRef, descendantRef string) (bool, error)
	Commit(ctx context.Context, message string, options ...string) error
	SubmoduleUpdate(ctx context.Context) error
	FileHashes(ctx context.Context, ref string) (map[string][]byte, error)
	GetCommitTimeSecond(ctx context.Context, ref string) (int64, error)
}

type impl struct {
	directory string
	runner    commandRunner
	logger    *zap.SugaredLogger
}

// New creates new Git interface implementation. A nil logger is tolerated and
// discards log output.
func New(directory string, logger *zap.SugaredLogger) Interface {
	if logger == nil {
		logger = zap.NewNop().Sugar()
	}
	return &impl{
		directory: directory,
		runner:    &osExecRunner{},
		logger:    logger,
	}
}

// wrapError wraps a non-nil err with the failing git command's arguments,
// additionally wrapping ErrFatal if the command exited with a fatal (128) or
// usage (129) exit code, as opposed to a non-fatal, conditional exit code
// such as 1, and ErrTimeout if ctx's own timeout (rather than a parent
// cancellation) has elapsed.
func wrapError(ctx context.Context, args []string, err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && isFatalExitCode(exitErr.ExitCode()) {
		err = fmt.Errorf("%w: %w", ErrFatal, err)
	}
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func isFatalExitCode(code int) bool {
	return code == 128 || code == 129
}

// Checkout checks out a specific reference in the repository.
func (c *impl) Checkout(ctx context.Context, ref string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"checkout", ref}, options...)
	return wrapError(ctx, args, c.runner.run(ctx, c.directory, "git", args...))
}

// Fetch runs git fetch for a remote ref.
func (c *impl) Fetch(ctx context.Context, remote, ref string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"fetch", remote, ref}, options...)
	return wrapError(ctx, args, c.runner.run(ctx, c.directory, "git", args...))
}

// Clone clones the target repository to the destination.
// The target repository can be either a remote repository or a local repository.
func (c *impl) Clone(ctx context.Context, target, destination string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append(append([]string{"clone"}, options...), target, destination)
	return wrapError(ctx, args, c.runner.run(ctx, c.directory, "git", args...))
}

// Diff returns the diff between two references.
func (c *impl) Diff(ctx context.Context, baseRef, targetRef string, options ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"diff", baseRef, targetRef}, options...)
	out, err := c.runner.output(ctx, c.directory, "git", args...)
	return out, wrapError(ctx, args, err)
}

// ApplyPatch applies a patch to the repository.
func (c *impl) ApplyPatch(ctx context.Context, patch []byte) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"apply", "--3way", "--whitespace", "nowarn", "--index", "-"}
	return wrapError(ctx, args, c.runner.runWithStdin(ctx, c.directory, "git", patch, args...))
}

// RevParse returns the revision hash of a reference.
func (c *impl) RevParse(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"rev-parse", ref}
	out, err := c.runner.output(ctx, c.directory, "git", args...)
	if err != nil {
		return "", wrapError(ctx, args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether ancestorRef is an ancestor of descendantRef.
func (c *impl) IsAncestor(ctx context.Context, ancestorRef, descendantRef string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"merge-base", "--is-ancestor", ancestorRef, descendantRef}
	_, err := c.runner.output(ctx, c.directory, "git", args...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		// an exit code other than 1, or a non-ExitError failure (context canceled,
		// git binary missing, I/O error), indicates the check itself failed.
		return false, wrapError(ctx, args, err)
	}
	return true, nil
}

// Commit commits the changes to the repository.
func (c *impl) Commit(ctx context.Context, message string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := append([]string{"commit", "-am", message}, options...)
	return wrapError(ctx, args, c.runner.run(ctx, c.directory, "git", args...))
}

// SubmoduleUpdate updates the submodules in the repository.
func (c *impl) SubmoduleUpdate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"submodule", "update", "--init", "--recursive"}
	return wrapError(ctx, args, c.runner.run(ctx, c.directory, "git", args...))
}

// DiffWithStatus returns the list of changed files with their status between two refs,
// parsed from `git diff --name-status`. For renames, Path is the destination path.
func (c *impl) DiffWithStatus(ctx context.Context, baseRef, targetRef string) ([]DiffEntry, error) {
	out, err := c.Diff(ctx, baseRef, targetRef, "--name-status", "-z")
	if err != nil {
		return nil, err
	}
	var entries []DiffEntry
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); {
		if len(fields[i]) == 0 {
			i++
			continue
		}
		status := string(fields[i])
		i++
		pathCount := 1
		if status[0] == 'R' || status[0] == 'C' {
			pathCount = 2
		}
		if i+pathCount > len(fields) {
			break
		}
		path := string(fields[i+pathCount-1])
		i += pathCount
		entries = append(entries, DiffEntry{Status: status[:1], Path: path})
	}
	return entries, nil
}

// GetCommitTimeSecond returns the commit timestamp of the given ref in Unix seconds.
func (c *impl) GetCommitTimeSecond(ctx context.Context, ref string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"log", "-1", "--format=%ct", ref}
	out, err := c.runner.output(ctx, c.directory, "git", args...)
	if err != nil {
		return 0, wrapError(ctx, args, err)
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// FileHashes gets a mapping of files to their hashes based on `git ls-tree --full-tree -r <ref>`.
func (c *impl) FileHashes(ctx context.Context, ref string) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	args := []string{"ls-tree", "--full-tree", "-r", "-z", ref}
	out, err := c.runner.output(ctx, c.directory, "git", args...)
	if err != nil {
		return nil, wrapError(ctx, args, err)
	}

	fileHashes := make(map[string][]byte)
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 {
			c.logger.Warnw("skipping ls-tree record due to unexpected format", "record", string(record))
			continue
		}
		metadata := strings.Fields(string(fields[0]))
		if len(metadata) < 3 {
			c.logger.Warnw("skipping ls-tree record due to unexpected metadata", "record", string(record))
			continue
		}
		hash, err := hex.DecodeString(metadata[2])
		if err != nil {
			c.logger.Warnw("skipping ls-tree record due to parsing error", "record", string(record), zap.Error(err))
			continue
		}
		fileHashes[string(fields[1])] = hash
	}
	return fileHashes, nil
}

// commandRunner abstracts command execution for testability.
type commandRunner interface {
	run(ctx context.Context, dir string, name string, args ...string) error
	output(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
	runWithStdin(ctx context.Context, dir string, name string, stdin []byte, args ...string) error
}

type osExecRunner struct{}

func (r *osExecRunner) run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := execcmd.CommandContext(ctx, name, args...)
	if errors.Is(cmd.Err, exec.ErrDot) {
		cmd.Err = nil
	}
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *osExecRunner) output(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := execcmd.CommandContext(ctx, name, args...)
	if errors.Is(cmd.Err, exec.ErrDot) {
		cmd.Err = nil
	}
	cmd.Dir = dir
	return cmd.Output()
}

func (r *osExecRunner) runWithStdin(ctx context.Context, dir string, name string, stdin []byte, args ...string) error {
	cmd := execcmd.CommandContext(ctx, name, args...)
	if errors.Is(cmd.Err, exec.ErrDot) {
		cmd.Err = nil
	}
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
