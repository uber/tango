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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/execcmd"
	"go.uber.org/zap"
)

const (
	// default query timeout if not provided in config
	_queryTimeout = 15 * time.Minute
)

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
	logger             *zap.SugaredLogger
	execCommandContext func(ctx context.Context, name string, arg ...string) commander
	queryTimeout       time.Duration
}

type Params struct {
	BazelCommand       string
	WorkspacePath      string
	EnvVarsMap         map[string]string
	Logger             *zap.SugaredLogger
	ExecCommandContext func(ctx context.Context, name string, arg ...string) commander
	QueryTimeout       time.Duration
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
		p.Logger.Errorw("NewBazelClient: Error detecting bazel executable", zap.Error(err))
		return nil, err
	}
	p.Logger.Info("NewBazelClient", zap.String("bazelCommand", bazelCommand), zap.String("workspacePath", p.WorkspacePath))
	return &BazelClient{
		workspacePath:      p.WorkspacePath,
		envVarsMap:         p.EnvVarsMap,
		bazelCommand:       bazelCommand,
		logger:             p.Logger,
		execCommandContext: execCmd,
		queryTimeout:       timeout,
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
func ensureBazelisk(ctx context.Context) (string, error) {
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
		return "", fmt.Errorf("download bazelisk: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download bazelisk: HTTP %d from %s", resp.StatusCode, url)
	}
	// Write to a temp file then atomically rename to avoid partial binaries.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".bazelisk-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write bazelisk: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", fmt.Errorf("chmod bazelisk: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", fmt.Errorf("install bazelisk: %w", err)
	}
	return dest, nil
}
