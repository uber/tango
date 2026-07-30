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
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/observability/metrics"
)

// toWireError converts err into a YARPC error carrying a TangoError detail so
// clients receive a classified error code on the wire. Implemented RPC
// handlers pass their return errors through this function (typically via the
// existing named-return defer) to satisfy the proto contract described in
// docs/errors/errors.md. Nil errors pass through unchanged.
func toWireError(err error) error {
	return mapper.ToProtoError(err)
}

// emitFailureMetric tags the failure counter with err's ErrorCode. e should
// already carry the repo tag; op is the operation subscope the counter lands under.
func emitFailureMetric(e *metrics.Emitter, op string, err error) {
	e.Tagged(map[string]string{
		"error_code": tangoerrors.GetErrorCode(err).String(),
	}).Counter(op, "failures").Inc(1)
}
