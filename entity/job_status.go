package entity

import (
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
)

// JobState is the lifecycle state of an asynchronous job.
type JobState int

const (
	// JobStateInvalid is the zero value. It is not a valid state.
	JobStateInvalid JobState = iota
	// JobStateRunning means a worker is computing the result.
	JobStateRunning
	// JobStateCompleted means the result is available.
	JobStateCompleted
	// JobStateFailed means the computation failed.
	JobStateFailed
)

// String returns the state name.
func (s JobState) String() string {
	switch s {
	case JobStateRunning:
		return "RUNNING"
	case JobStateCompleted:
		return "COMPLETED"
	case JobStateFailed:
		return "FAILED"
	default:
		return "INVALID"
	}
}

// JobStaleAfter returns the age of LastUpdated after which a RUNNING job has no
// live worker. heartbeatInterval is the time between two writes of a running job.
func JobStaleAfter(heartbeatInterval time.Duration) time.Duration {
	return 3 * heartbeatInterval
}

// JobStatus is the state of an asynchronous job.
type JobStatus struct {
	State JobState `json:"state"`
	// ErrorCode and ErrorMessage describe the failure.
	ErrorCode    tangoerrors.ErrorCode `json:"error_code,omitempty"`
	ErrorMessage string                `json:"error_message,omitempty"`
	// LastUpdated is the time of the last write. A running job renews it every
	// heartbeat interval. For a COMPLETED or FAILED job it is the end time.
	LastUpdated time.Time `json:"last_updated"`
	// RepositoryID is the repository of the job.
	RepositoryID string `json:"repository_id"`
	// ResultKey is the storage key of the result. Set only when State is COMPLETED.
	ResultKey string `json:"result_key,omitempty"`
	// GraphFormat is the format of the target graph at ResultKey. Set only for a
	// COMPLETED target-graph job.
	GraphFormat string `json:"graph_format,omitempty"`
}
