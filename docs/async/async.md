# RFC: Asynchronous Tango RPCs

## Summary

Today, each Tango RPC holds the connection open until the full target graph or changed-target computation finishes. This RFC adds an asynchronous API for `GetTargetGraph` and `GetChangedTargets`. Each operation gets two RPCs. The submit RPC takes the existing request message, starts a job, and returns a token at once. The poll RPC takes the token and returns the job status and, when the job is complete, the result. The existing synchronous RPCs do not change.

## Problem

- A cold computation can take many minutes. The client must keep a stream open for the full duration.
- A client disconnect cancels the request work, so the next attempt starts from zero.

## Design

1. `GetTargetGraph` and `GetChangedTargets` each get a submit RPC and a poll RPC. The submit RPC reuses the existing request message. The poll RPC takes the token and an optional `OutputConfig`. The existing synchronous RPCs keep their current behavior.
2. Each request maps to a **token**. The token is a deterministic hash of the build descriptions, the extra exclusion regexes, and `bypass_cache`. So a `bypass_cache` job and a normal job of the same request are separate jobs. The token excludes `output_config`, because it does not change the computed result. The submit returns the token, and the client polls with it. The token is part of the storage key of the job status. The key has no repository ID, because the build descriptions in the token include the remote. Tango adds the token as a `job_token` field to the log lines of each submit, poll, and job run, so we can find the token when debugging a request.
3. A job has one of three states: `RUNNING`, `COMPLETED`, or `FAILED`.
4. For a normal job, a submit after the job completes returns `COMPLETED` at once, if the result is still in storage. If the result is gone, the submit starts a new job. For a `bypass_cache` job, each submit after the job finishes computes again. So if a result is corrupt, the client submits with `bypass_cache`, which computes the result again and overwrites it.
5. The submit RPC starts a failed job again, so the client retries a failure with a new submit.
6. The poll RPC only reads. It never starts work. It returns the job status and, after `COMPLETED`, the result. When the job completes, the job writes the storage key of the result into the job status. So poll reads the result directly, and it does not look up the treehash of the request again. An unknown token reads as not found.
7. The submit RPC requires a request deadline. If the request has none, the submit returns a user error. The timeout of the submit request is the job timeout. The job goroutine runs with this timeout.
8. A running job renews its status once per heartbeat interval. The interval is the `service.job_heartbeat_interval` config, with a default of 5 seconds. A `RUNNING` status that has not changed for 3 intervals has no live worker. The limit of 3 intervals lets a live job miss one heartbeat when its storage writes are slow, because each write can take up to one interval. It reads as no job, so a new submit starts a new job. This releases a job whose process stopped or crashed, and Tango needs no shutdown hook for it.
9. One goroutine writes the status of a job after the submit. It writes the heartbeats and the final status, so a late heartbeat cannot overwrite `COMPLETED` or `FAILED`.
10. A changed-targets job runs one target-graph job for each revision. Each graph job has the token that `SubmitTargetGraphJob` computes for that revision. So a changed-targets job shares a graph job with a direct target-graph submit, or with another changed-targets job for the same revision. The changed-targets job submits each graph job. If the submit returns `COMPLETED`, the changed-targets job uses the result. Otherwise, it reads the status of the graph job once per heartbeat interval. If a graph job fails or has no live worker, the changed-targets job fails. It does not submit the graph job again. The other graph job continues, because other callers can wait on it. The poll RPC reports only the state of the changed-targets job.

## Wire contract

The submit RPC returns the token and the job status after the submit. The poll RPC returns one message. The message always has the `JobStatus`. If the state is `COMPLETED`, the message also has the full result. For all other states, it has only the `JobStatus`.

The poll RPCs return one message and do not stream like the synchronous RPCs. One message is easier for the client to use. A streamed result needs the client to merge the chunks before it can use them. If a stream fails midway, the client cannot use the partial result, so it must retry. A unary call that fails also needs a retry, so unary loses nothing here. The cost is the message size. In TerraBlob, the biggest TGB graphs are about 148 MB, and the biggest changed-targets blobs are about 794 MB. These sizes come from samples of the blobs that Tango wrote from 2026-08-10 to 2026-10-09. The wire size is smaller: the largest that we measured was 436 MB. This fits under the protobuf limit of 2 GiB, but each client must raise its receive limit above the 64 MiB default.

```proto
enum JobState {
    JOB_STATE_INVALID = 0;
    JOB_STATE_RUNNING = 1;
    JOB_STATE_COMPLETED = 2;
    JOB_STATE_FAILED = 3;
}

message JobStatus {
    JobState state = 1;
    // Set only when state is JOB_STATE_FAILED.
    TangoError error = 2;
}

message SubmitJobResponse {
    string token = 1;
    JobStatus status = 2;
}

message PollJobRequest {
    string token = 1;
    OutputConfig output_config = 2;
}

message PollTargetGraphJobResponse {
    OptimizedTargets targets = 1;
    Metadata metadata = 2;
    JobStatus status = 3;
}

message PollChangedTargetsJobResponse {
    ChangedTargets changed_targets = 1;
    Metadata metadata = 2;
    JobStatus status = 3;
}

service Tango {
    rpc SubmitTargetGraphJob(GetTargetGraphRequest) returns (SubmitJobResponse) {}
    rpc PollTargetGraphJob(PollJobRequest) returns (PollTargetGraphJobResponse) {}
    rpc SubmitChangedTargetsJob(GetChangedTargetsRequest) returns (SubmitJobResponse) {}
    rpc PollChangedTargetsJob(PollJobRequest) returns (PollChangedTargetsJobResponse) {}
}
```

## Job states

| State | Meaning | Result in poll response |
|---|---|---|
| `RUNNING` | A worker is computing the result. | None |
| `COMPLETED` | The result is available. | Full result in the same message |
| `FAILED` | The computation failed. | `TangoError` with the classified error code |

A changed-targets job stays `RUNNING` while its graph jobs run. It becomes `FAILED` if one of its graph jobs fails or has no live worker.

```
                  submit
   ┌────────┐ ─────────────► ┌─────────┐
   │ no job │                │ RUNNING │
   └────────┘ ◄───────────── └─────────┘
              no heartbeat      │     │
              for 3 intervals   │     │ error, or job timeout
                    result ready│     │
                                ▼     ▼
                       ┌───────────┐ ┌────────┐
                       │ COMPLETED │ │ FAILED │
                       └───────────┘ └────────┘
                                │     │
                                └──┬──┘
                                   │ submit: starts a new job, except for
                                   │ COMPLETED with the result in storage
                                   ▼
                                RUNNING
```

## Job Status

```go
// JobState is the lifecycle state of an asynchronous job.
type JobState int

const (
	JobStateInvalid JobState = iota
	JobStateRunning
	JobStateCompleted
	JobStateFailed
)

// JobStatus is the state of an asynchronous job.
type JobStatus struct {
	State JobState
	// ErrorCode and ErrorMessage describe the failure.
	ErrorCode    tangoerrors.ErrorCode
	ErrorMessage string
	// LastUpdated is the time of the last write. A running job renews it
	// once per heartbeat interval. For a COMPLETED or FAILED job it is the end time.
	LastUpdated time.Time
	// RepositoryID is the repository of the job.
	RepositoryID string
	// ResultKey is the storage key of the result. Set only when COMPLETED.
	ResultKey string
	// GraphFormat is the format of the graph at ResultKey: gob or TGB.
	// Set only for a COMPLETED target-graph job.
	GraphFormat string
}
```

## Request flow

The submit and poll RPCs for each of `GetChangedTargets` and `GetTargetGraph` will follow the general flow below.

```
func submit(request):
  validate(request)
  token = hash(computation inputs and bypass_cache of request)
  status, found = readJobStatus(token)
  if found and status is RUNNING:
    return token, status
  if found and status is COMPLETED and not request.bypass_cache and exists(status.result key):
    return token, status
  writeJobStatus(token, RUNNING)
  go runJob(token, request, deadline of the submit request)
  return token, RUNNING

func runJob(token, request, deadline):
  go compute(request) and send the result key and the error to the channel done
  loop:
    every interval:    writeJobStatus(token, RUNNING)
    on done:           stop the loop
    on deadline:       err = deadline error, stop the loop
  if err:
    writeJobStatus(token, FAILED, err)
  else:
    writeJobStatus(token, COMPLETED, result key)

func computeTargetGraph(request):
  getGraph(request)  # reads the graph cache unless bypass_cache, else computes and writes it
  return key of the stored graph

func computeChangedTargets(request):
  if not request.bypass_cache:
    key, found = readResultCache(request)
    if found:
      return key
  for each revision, in parallel:
    graphToken, status = submitTargetGraph(revision of request)
    if status is not COMPLETED:
      status = waitForJob(graphToken)  # reads every interval; FAILED or not found is an error
    graph = read(status.result key)
  result = compare(graphs)
  return writeResultCache(request, result)

func poll(token, outputConfig):
  status, found = readJobStatus(token)
  if not found:
    return not-found error
  if status is not COMPLETED:
    return status
  result, found = read(status.result key)
  if found:
    return COMPLETED, filter(result, outputConfig)
  return not-found error
```

## Storage

Any `core/storage.Storage` backend Tango already supports (disk, in-memory, or a remote object store) can hold job status. Job status uses a separate `controller.Params.JobStorage`, which defaults to the storage of the result caches. No new storage system is introduced.

Each job is one record. `core/cachekey` builds the key, and `core/storage` encodes the value.

| Part | Format |
|---|---|
| Key of a target-graph job | `jobs/target-graph/<token>` |
| Key of a changed-targets job | `jobs/changed-targets/<token>` |
| Token | The MD5 hex digest of the computation inputs and `bypass_cache` |
| Value | The JSON encoding of `entity.JobStatus` |

The value has these fields:

| Field | Type | Description |
|---|---|---|
| `state` | integer | 1 `RUNNING`, 2 `COMPLETED`, 3 `FAILED` |
| `error_code` | integer | The `tangoerrors.ErrorCode` of a failure: 0 infra, 1 cancelled, 2 user, 3 retryable infra. Omitted when 0. |
| `error_message` | string | The message of a failure. Omitted when empty. |
| `last_updated` | RFC 3339 time | The time of the last write. A running job renews it once per heartbeat interval. For `COMPLETED` and `FAILED`, it is the end time. |
| `repository_id` | string | The repository of the job. Poll uses it for metrics and logs. |
| `result_key` | string | The storage key of the result. Set only for `COMPLETED`. |
| `graph_format` | string | `gob` or `tgb`, the format of the graph at `result_key`. Set only for a `COMPLETED` target-graph job. |

```json
{"state":1,"last_updated":"2026-10-07T18:30:00Z","repository_id":"go-code"}
{"state":2,"last_updated":"2026-10-07T18:34:40Z","repository_id":"go-code","result_key":"go-code/graphs/3f2a.../NATIVE-tgb","graph_format":"tgb"}
{"state":3,"error_code":1,"error_message":"app context canceled","last_updated":"2026-10-07T18:31:12Z","repository_id":"go-code"}
```

The record has no TTL in the store. A reader treats a `RUNNING` record as not found when `last_updated` is more than 3 heartbeat intervals old. `COMPLETED` and `FAILED` records do not expire, so the store must limit their number, for example with LRU eviction.

The ideal store for job status has these characteristics:

- **Fast reads and writes of small records.** A job status is a small key-value record. Every waiting client reads it once per poll interval. Each running job writes it once per heartbeat interval, and each changed-targets job reads the records of its two graph jobs once per interval. So the store must serve many small reads and frequent small writes with low latency.
- **No need for durability.** A lost status reads as "no job", and the next submit starts the job again. The results stay in the durable result cache, so a new job for a completed request reuses the cached result at once. The store can trade durability for speed.

Redis is an example of a store that fits these criteria.

## Client usage: changed targets

The client submits once, then polls with the token until the status is terminal. After `COMPLETED`, the same response holds the result. To retry after `FAILED`, the client can submit again.

```go
func getChangedTargets(ctx context.Context, client pb.TangoYARPCClient, req *pb.GetChangedTargetsRequest) (*pb.PollChangedTargetsJobResponse, error) {
    submitted, err := client.SubmitChangedTargetsJob(ctx, req)
    if err != nil {
        return nil, err
    }
    poll := &pb.PollJobRequest{Token: submitted.GetToken(), OutputConfig: req.GetOutputConfig()}
    for {
        resp, err := client.PollChangedTargetsJob(ctx, poll)
        if err != nil {
            return nil, err
        }
        switch status := resp.GetStatus(); status.GetState() {
        case pb.JOB_STATE_COMPLETED:
            return resp, nil
        case pb.JOB_STATE_FAILED:
            return nil, fmt.Errorf("job failed: %s", status.GetError().GetMessage())
        }
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-time.After(pollInterval):
        }
    }
}
```
