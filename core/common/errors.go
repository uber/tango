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

package common

// Error type values for the failure_type metrics tag.
const (
	ErrorTypeUser  = "user"
	ErrorTypeInfra = "infra"
)

// ClassifiedError is an error that carries an explicit failure reason and type
// for metrics classification. External clients can implement this interface so
// that classifyError in the controller picks it up automatically via errors.As,
// without any changes to the classification logic.
type ClassifiedError interface {
	error
	// Reason returns the failure_reason tag value (e.g. "send", "graph_fetch").
	Reason() string
	// Type returns the failure_type tag value: ErrorTypeUser or ErrorTypeInfra.
	Type() string
	Unwrap() error
}

// classifiedErr is the package-internal concrete implementation.
type classifiedErr struct {
	reason    string
	errorType string
	err       error
}

func (e *classifiedErr) Error() string  { return e.err.Error() }
func (e *classifiedErr) Unwrap() error  { return e.err }
func (e *classifiedErr) Reason() string { return e.reason }
func (e *classifiedErr) Type() string   { return e.errorType }

// WithReason wraps err with an explicit failure reason and type for metrics classification.
func WithReason(reason, errorType string, err error) ClassifiedError {
	return &classifiedErr{reason: reason, errorType: errorType, err: err}
}
