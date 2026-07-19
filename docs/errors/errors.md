# Tango Errors

## Overview

Tango's API contract currently does not contain error information and within Tango there is no standard for how errors are returned and classified.

To solve this issue, we define a set of error codes that will be part of Tango's proto contract and a package `core/errors` to help classify errors.

This document covers the proto contract and the exported API of `core/errors`.

## Proto

`tango/proto/tango.proto` defines the wire-level error type returned by every RPC when it fails.

```protobuf
enum ErrorCode {
    ERROR_INFRA = 0;
    ERROR_CANCELLED = 1;
    ERROR_USER = 2;
    ERROR_INFRA_RETRYABLE = 3;
}

message TangoError {
    ErrorCode code = 1;
    String message = 2;
}
```

| Error Code | Definition |
|---|---|
| `ERROR_INFRA` | Infra failures within Tango APIs not caused by the user |
| `ERROR_CANCELLED` | Client canceled the request |
| `ERROR_USER` | Failures caused by user input such as request validation errors or build graph failures |
| `ERROR_INFRA_RETRYABLE` | A subset of infra errors that can be retried and expected to succeed |

## Package `core/errors`

Package `errors` defines `TangoError`, Tango's internal error type. It provides constructors so callers can wrap their errors with a `TangoError`.

### Index

- [type ErrorCode](#type-errorcode)
  - [func (ErrorCode) String() string](#func-errorcode-string)
- [type TangoError](#type-tangoerror)
  - [func NewInfra(err error) error](#func-newinfra)
  - [func NewUser(err error) error](#func-newuser)
  - [func NewInfraRetryable(err error) error](#func-newinfraretryable)
  - [func (\*TangoError) Error() string](#func-tangoerror-error)
  - [func (\*TangoError) Unwrap() error](#func-tangoerror-unwrap)
- [func GetErrorCode(err error) ErrorCode](#func-geterrorcode)

### type ErrorCode

```go
type ErrorCode int
```

`ErrorCode` mirrors the proto `ErrorCode` enum and classifies a `TangoError` as a user error or an infra error (retryable or not).

```go
const (
	// ErrorInfra represents infra failures within Tango APIs not caused by the user.
	ErrorInfra ErrorCode = iota
	// ErrorCancelled represents a cancelled request.
	ErrorCancelled
	// ErrorUser represents request validation errors and user failures in builds.
	ErrorUser
	// ErrorInfraRetryable represents a subset of infra errors that can be retried and expected to succeed.
	ErrorInfraRetryable
)
```

#### func (ErrorCode) String

```go
func (code ErrorCode) String() string
```

String returns the string form of the code: `"cancelled"`, `"user"`, `"infra_retryable"`, or `"infra"` as the default.

### type TangoError

```go
type TangoError struct {
	err       error
	errorCode ErrorCode
}
```

`TangoError` is Tango's internal error type, carrying the underlying error and its `ErrorCode`. The `internal/mapper` package uses the error and error code to build the proto `TangoError` for the RPC response, and metrics and logging use the error code as a tag.

#### func (\*TangoError) Error

```go
func (te *TangoError) Error() string
```

Error returns the underlying error's message.

#### func (\*TangoError) Unwrap

```go
func (te *TangoError) Unwrap() error
```

Unwrap returns the underlying error, so `errors.Is` / `errors.As` can traverse a `TangoError`.

The three constructors below build a `TangoError` from an error, classifying it with a fixed `ErrorCode`. In all three, if `err` already wraps a `TangoError`, the passed in error code will overwrite the existing error code.

#### func NewInfra

```go
func NewInfra(err error) error
```

NewInfra wraps err as a `TangoError` classified [`ERROR_INFRA`](#type-errorcode).

#### func NewUser

```go
func NewUser(err error) error
```

NewUser wraps err as a `TangoError` classified [`ERROR_USER`](#type-errorcode).

#### func NewInfraRetryable

```go
func NewInfraRetryable(err error) error
```

NewInfraRetryable wraps err as a `TangoError` classified [`ERROR_INFRA_RETRYABLE`](#type-errorcode).

### func GetErrorCode

```go
func GetErrorCode(err error) ErrorCode
```

GetErrorCode extracts the `ErrorCode` from err. If err is `context.Canceled`, `ErrorCancelled` is returned. If err wraps a `TangoError`, its code is returned. Otherwise `ErrorInfra` is returned.

## Usage

The constructors are meant to be used only in top level layers (controller, orchestrator, graphrunner) that call components (git, bazel, targethasher, storage, etc). The components used by these layers are responsible for providing sentinel errors if they can be classified as user or infra-retryable. Plain errors will be classified as infra by default in the controller.

```go
// bazel
var ErrDownloadBazeliskNetwork = errors.New("download bazelisk network failure")

func ensureBazelisk(...) (..., error) {
	...
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) {
			return "", fmt.Errorf("%w: %w", ErrDownloadBazeliskNetwork, err)
		}
		return "", err
	}
	...
}

// orchestrator
func classifyBazelClientError(err error) error {
	wrappedErr := fmt.Errorf("create bazel client: %w", err)
	if errors.Is(wrappedErr, bazel.ErrDownloadBazeliskNetwork) {
		return tangoerrors.NewInfraRetryable(wrappedErr)
	}
	// check other sentinels if any
	return wrappedErr
}

func (b *nativeOrchestrator) GetTargetGraph(...) (..., error) {
	client, err := bazel.NewBazelClient(...)
	if err != nil {
		return nil, classifyBazelClientError(err)
	}

// controller
func (c *controller) GetChangedTargets(...) error {
	...
	if err != nil {
		return mapper.ToProtoError(err)
	}
	return nil
}
```
