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

package bazel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/execcmd"
	"go.uber.org/zap"
)

const (
	// default query timeout if not provided in config
	_queryTimeout = 15 * time.Minute
)

// _moduleValueFlags are the flags that take a value, as `--flag=value` or
// `--flag value`, and change the output of `bazel mod dump_repo_mapping`:
// --registry and --override_module decide which modules, and in some Bazel
// versions which canonical names, module resolution produces, and
// --inject_repository adds a repo to the main repo's mapping.
// Cache locations (--repository_cache, --distdir, ...) and --config are
// deliberately absent: the former leave the mapping unchanged, and a config
// defined only for `query` makes `bazel mod` fail.
var _moduleValueFlags = map[string]struct{}{
	"registry":          {},
	"override_module":   {},
	"inject_repository": {},
}

// _moduleBoolFlags are the boolean flags that change the mapping: whether
// WORKSPACE repos take part, and whether the root module's dev dependencies do;
// the `--no` form is matched as well. --enable_bzlmod is absent because
// `bazel mod` refuses to run without it.
var _moduleBoolFlags = map[string]struct{}{
	"enable_workspace":      {},
	"ignore_dev_dependency": {},
}

// ErrNetwork indicates an operation failed due to a transient network
// failure, as opposed to a permanent failure (e.g. a non-200 response).
// Retrying is expected to succeed.
var ErrNetwork = errors.New("network failure")

type QueryRequest struct {
	Query          string
	StartupOptions []string
	AdditionalArgs []string
}

type QueryResponse struct {
	Result *buildpb.QueryResult
}

type Bazel interface {
	ExecuteQuery(ctx context.Context, req *QueryRequest) (*QueryResponse, error)
}

// BazelClient is a client for interacting with Bazel.
type BazelClient struct {
	workspacePath      string
	envVarsMap         map[string]string
	bazelCommand       string
	logger             *zap.Logger
	execCommandContext func(ctx context.Context, name string, arg ...string) commander
	queryTimeout       time.Duration
	streamLogs         bool
}

type Params struct {
	BazelCommand       string
	WorkspacePath      string
	EnvVarsMap         map[string]string
	Logger             *zap.Logger
	ExecCommandContext func(ctx context.Context, name string, arg ...string) commander
	QueryTimeout       time.Duration
	StreamLogs         bool
}

func NewBazelClient(ctx context.Context, p Params) (*BazelClient, error) {
	execCmd := p.ExecCommandContext
	if execCmd == nil {
		execCmd = func(ctx context.Context, name string, arg ...string) commander {
			cmd := execcmd.CommandContext(ctx, name, arg...)
			cmd.Dir = p.WorkspacePath
			for key, value := range p.EnvVarsMap {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			cmd.Stdin = nil
			return cmd
		}
	}
	timeout := p.QueryTimeout
	if timeout == 0 {
		timeout = _queryTimeout
	}
	bazelCommand, err := detectBazelExecutable(ctx, p.BazelCommand)
	if err != nil {
		return nil, fmt.Errorf("detect bazel executable: %w", err)
	}
	p.Logger.Debug("NewBazelClient", zap.String("bazelCommand", bazelCommand), zap.String("workspacePath", p.WorkspacePath))
	return &BazelClient{
		workspacePath:      p.WorkspacePath,
		envVarsMap:         p.EnvVarsMap,
		bazelCommand:       bazelCommand,
		logger:             p.Logger,
		execCommandContext: execCmd,
		queryTimeout:       timeout,
		streamLogs:         p.StreamLogs,
	}, nil
}

// detectBazelExecutable returns the path to a bazelisk binary.
// If bazelCommand is explicitly provided, it is used as-is.
// Otherwise, bazelisk is downloaded from GitHub into a local cache directory.
func detectBazelExecutable(ctx context.Context, bazelCommand string) (string, error) {
	if bazelCommand != "" {
		return bazelCommand, nil
	}
	return ensureBazelisk(ctx)
}

// ensureBazelisk returns the path to a cached bazelisk binary,
func ensureBazelisk(ctx context.Context) (_ string, retErr error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cache dir: %w", err)
	}
	dir := filepath.Join(cacheDir, "tango", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}
	dest := filepath.Join(dir, "bazelisk")
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	url := fmt.Sprintf(
		"https://github.com/bazelbuild/bazelisk/releases/latest/download/bazelisk-%s-%s",
		runtime.GOOS, runtime.GOARCH,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build bazelisk request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) {
			return "", fmt.Errorf("download bazelisk: %w: %w", ErrNetwork, err)
		}
		return "", fmt.Errorf("download bazelisk: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download bazelisk: HTTP %d from %s", resp.StatusCode, url)
	}
	// Write to a temp file then atomically rename to avoid partial binaries.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".bazelisk-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// On failure, remove the temp file and fold any removal error into the
	// returned error. On the success path the file has been renamed into
	// place, so there is nothing to remove.
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, os.Remove(tmpPath))
		}
	}()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return "", fmt.Errorf("write bazelisk: %w", errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close bazelisk temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", fmt.Errorf("chmod bazelisk: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", fmt.Errorf("install bazelisk: %w", err)
	}
	return dest, nil
}

// OutputBase resolves the bazel executable and runs `bazel info output_base`,
// returning the absolute path to the output base directory. startupOptions must
// be the startup options the query ran with: --output_base and
// --output_user_root change where Bazel keeps its output base, so omitting them
// reports a directory the query never wrote to.
func OutputBase(
	ctx context.Context,
	workspacePath, bazelCommand string,
	startupOptions []string,
) (string, error) {
	resolved, err := detectBazelExecutable(ctx, bazelCommand)
	if err != nil {
		return "", fmt.Errorf("detect bazel: %w", err)
	}
	args := append(append([]string(nil), startupOptions...), "info", "output_base")
	cmd := execcmd.CommandContext(ctx, resolved, args...)
	cmd.Dir = workspacePath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bazel info output_base: %w", err)
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", fmt.Errorf("bazel info output_base returned empty path")
	}
	return result, nil
}

// RepoMapping returns the main repository's mapping from apparent repo names (`@name//...`) to
// canonical repo names (`@@canonical//...`), using `bazel mod dump_repo_mapping`.
// startupOptions are the startup options the query ran with, so the command reuses
// the same Bazel server. extraArgs are the query's extra arguments; only those that
// affect module resolution (see ModuleResolutionArgs) are forwarded.
func RepoMapping(
	ctx context.Context,
	workspacePath, bazelCommand string,
	startupOptions, extraArgs []string,
) (map[string]string, error) {
	resolved, err := detectBazelExecutable(ctx, bazelCommand)
	if err != nil {
		return nil, fmt.Errorf("detect bazel: %w", err)
	}
	args := append([]string(nil), startupOptions...)
	args = append(args, "mod")
	args = append(args, ModuleResolutionArgs(extraArgs)...)
	// The empty repo name selects the main repository.
	args = append(args, "dump_repo_mapping", "")
	cmd := execcmd.CommandContext(ctx, resolved, args...)
	cmd.Dir = workspacePath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bazel mod dump_repo_mapping: %w", err)
	}
	var mapping map[string]string
	if err := json.Unmarshal(out, &mapping); err != nil {
		return nil, fmt.Errorf("parse bazel mod dump_repo_mapping output: %w", err)
	}
	return mapping, nil
}

// ModuleResolutionArgs returns the subset of args that influence Bazel module
// resolution, in their original order. `bazel mod` rejects query-only flags, so
// the query's extra arguments cannot be forwarded to it wholesale.
func ModuleResolutionArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if _, ok := _moduleValueFlags[name]; ok {
			out = append(out, args[i])
			if !hasValue && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			continue
		}
		if _, ok := _moduleBoolFlags[strings.TrimPrefix(name, "no")]; ok && !hasValue {
			out = append(out, args[i])
		}
	}
	return out
}
