// Copyright (c) 2026 Uber Technologies, Inc.
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

package metrics

import "context"

type emitterKey struct{}

var noopEmitter = New(nil)

// WithEmitter installs e on ctx so downstream helpers can retrieve it with
// FromContext.
func WithEmitter(ctx context.Context, e *Emitter) context.Context {
	return context.WithValue(ctx, emitterKey{}, e)
}

// FromContext returns the installed Emitter, or a noop if none. Never returns nil.
func FromContext(ctx context.Context) *Emitter {
	if ctx == nil {
		return noopEmitter
	}
	e, ok := ctx.Value(emitterKey{}).(*Emitter)
	if !ok || e == nil {
		return noopEmitter
	}
	return e
}
