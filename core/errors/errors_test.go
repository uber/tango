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

package errors

import (
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	inner := stderrors.New("something went wrong")
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	assert.Equal(t, ErrorTypeInfra, ce.ErrorType)
	assert.Equal(t, FailureReasonUnknown, ce.Reason)
	assert.Equal(t, inner, ce.Err)
}

func TestClassifiedError_AsTraversal(t *testing.T) {
	// errors.As should find *ClassifiedError through a wrapping fmt.Errorf.
	inner := stderrors.New("root cause")
	ce := New(ErrorTypeUser, FailureReasonValidation, inner)
	wrapped := fmt.Errorf("outer: %w", ce)

	var found *ClassifiedError
	assert.True(t, stderrors.As(wrapped, &found))
	assert.Equal(t, ErrorTypeUser, found.ErrorType)
	assert.Equal(t, FailureReasonValidation, found.Reason)
	assert.Equal(t, inner, found.Err)
}

func TestClassifiedError_StructuredInnerAsTraversal(t *testing.T) {
	// errors.As should traverse ClassifiedError to find the inner structured type.
	inner := &ErrDownloadGraph{Key: "itg/abc", Cause: stderrors.New("io error")}
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	var found *ErrDownloadGraph
	assert.True(t, stderrors.As(ce, &found))
	assert.Equal(t, "itg/abc", found.Key)
}

func TestClassifiedError_IsTraversal(t *testing.T) {
	// errors.Is should traverse ClassifiedError and into the structured type's Cause.
	root := stderrors.New("root cause")
	inner := &ErrDownloadGraph{Key: "k", Cause: root}
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	assert.True(t, stderrors.Is(ce, root))
}

func TestClassifiedError_IsTraversalStructured(t *testing.T) {
	// errors.Is should match the structured type itself when it is the target.
	inner := &ErrDownloadGraph{Key: "k", Cause: stderrors.New("io error")}
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	assert.True(t, stderrors.Is(ce, inner))
}
