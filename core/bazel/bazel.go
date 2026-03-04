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
	"os"
	"os/exec"
	"time"

	"fmt"
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
	return &BazelClient{
		workspacePath:      p.WorkspacePath,
		envVarsMap:         p.EnvVarsMap,
		bazelCommand:       bazelCommand,
		logger:             p.Logger,
		execCommandContext: execCmd,
		queryTimeout:       timeout,
	}, nil
}

// detectBazelExecutable detects the bazel executable path.
// If bazelCommand is provided, it returns the bazelCommand.
// Otherwise, it looks for the bazel executable in the PATH.
// If the bazel executable is not found, it returns an error.
func detectBazelExecutable(bazelCommand string) (string, error) {
	if bazelCommand != "" {
		return bazelCommand, nil
	}
	// Most correct: honor Bazelisk wrapper env vars
	if p, ok := bazelFromEnv(); ok {
		return p, nil
	}

	// Otherwise fall back to PATH search
	path, err := exec.LookPath("bazel")
	if err != nil {
		return "", fmt.Errorf("could not locate bazel: %w", err)
	}
	return path, nil
}

func bazelFromEnv() (string, bool) {
	for _, key := range []string{"BAZEL_REAL", "BAZELISK_BIN", "BAZEL"} {
		if v := os.Getenv(key); v != "" {
			return v, true
		}
	}
	return "", false
}
