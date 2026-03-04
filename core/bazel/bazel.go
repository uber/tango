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
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
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

func NewBazelClient(p Params) (*BazelClient, error) {
	execCmd := p.ExecCommandContext
	if execCmd == nil {
		execCmd = func(ctx context.Context, name string, arg ...string) commander {
			cmd := exec.CommandContext(ctx, name, arg...)
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
	bazelCommand, err := detectBazelExecutable(p.BazelCommand)
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
func detectBazelExecutable(bazelCommand string) (string, error) {
	if bazelCommand != "" {
		return bazelCommand, nil
	}
	return ensureBazelisk()
}

// ensureBazelisk returns the path to a cached bazelisk binary,
// downloading it from GitHub releases if it doesn't already exist.
func ensureBazelisk() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("could not determine cache directory: %w", err)
	}
	dir := filepath.Join(cacheDir, "tango", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create cache directory: %w", err)
	}

	binName := "bazelisk"
	if runtime.GOOS == "windows" {
		binName = "bazelisk.exe"
	}
	dest := filepath.Join(dir, binName)

	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}

	arch := runtime.GOARCH
	if arch == "aarch64" {
		arch = "arm64"
	}
	url := fmt.Sprintf(
		"https://github.com/bazelbuild/bazelisk/releases/latest/download/bazelisk-%s-%s",
		runtime.GOOS, arch,
	)

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to download bazelisk from %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download bazelisk from %s: HTTP %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(dir, "bazelisk-download-*")
	if err != nil {
		return "", fmt.Errorf("could not create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// Clean up temp file on any failure path.
		os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("failed writing bazelisk binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("failed closing bazelisk binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", fmt.Errorf("failed to chmod bazelisk binary: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", fmt.Errorf("failed to move bazelisk binary into place: %w", err)
	}
	return dest, nil
}
