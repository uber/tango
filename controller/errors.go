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

package controller

import (
	"context"
	"errors"
	"strings"
)

// classifyError maps an error to a failure_reason tag value for metrics.
// Keep the failure_type derivation in sync: "cancelled" and "deadline_exceeded"
// are user-attributed; everything else is infra.
func classifyError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	// gRPC/YARPC wraps HTTP 502 responses from upstream proxies as code:unavailable
	// with the text "Bad Gateway". Distinguish these from generic unknown infra errors
	// so dashboards can separate proxy/LB blips from actual server failures.
	if strings.Contains(err.Error(), "Bad Gateway") {
		return "gateway_unavailable"
	}
	return "unknown"
}
