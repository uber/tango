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
	"bufio"
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
	args := append(append([]string{"clone"}, options...), target, destination)
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
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether ancestorRef is an ancestor of descendantRef.
func (c *impl) IsAncestor(ctx context.Context, ancestorRef, descendantRef string) (bool, error) {
	_, err := c.runner.output(ctx, c.directory, "git", "merge-base", "--is-ancestor", ancestorRef, descendantRef)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		// an exit code other than 1, or a non-ExitError failure (context canceled,
		// git binary missing, I/O error), indicates the check itself failed.
		return false, fmt.Errorf("check if ref %s is ancestor of %s: %w", ancestorRef, descendantRef, err)
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

// DiffWithStatus returns the list of changed files with their status between two refs,
// parsed from `git diff --name-status`. For renames, Path is the destination path.
func (c *impl) DiffWithStatus(ctx context.Context, baseRef, targetRef string) ([]DiffEntry, error) {
	out, err := c.Diff(ctx, baseRef, targetRef, "--name-status")
	if err != nil {
		return nil, err
	}
	var entries []DiffEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Status may have a score suffix (e.g. "R100") — use only the first character.
		status := string(fields[0][0])
		// For renames/copies there are two paths; use the destination (last field).
		path := fields[len(fields)-1]
		entries = append(entries, DiffEntry{Status: status, Path: path})
	}
	return entries, nil
}

// GetCommitTimeSecond returns the commit timestamp of the given ref in Unix seconds.
func (c *impl) GetCommitTimeSecond(ctx context.Context, ref string) (int64, error) {
	out, err := c.runner.output(ctx, c.directory, "git", "log", "-1", "--format=%ct", ref)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
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
			c.logger.Warnw("skipping ls-tree line due to unexpected format", "line", line)
			continue
		}
		hash, err := hex.DecodeString(parts[2])
		if err != nil {
			c.logger.Warnw("skipping ls-tree line due to parsing error", "line", line, zap.Error(err))
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
