# Recovery

## Worker Failure

```text
heartbeat timeout
  → worker = OFFLINE
  → assigned/running tasks become ORPHANED
  → inspect last known attempt
  → verify output artifacts when possible
  → cleanup incomplete staging outputs
  → apply retry policy
  → requeue eligible tasks
  → scheduler assigns to another worker
```

## Master Crash

```text
master restart
  → open durable store
  → load active jobs and tasks
  → rebuild in-memory queues
  → discover/reconcile workers
  → expire stale leases
  → mark orphaned work
  → resume scheduler
```

## Database Disconnect

Workers must not kill active media processes solely because the master database is temporarily unavailable.

Target behavior:

```text
RUNNING
  → database/master unavailable
  → keep local state
  → continue FFmpeg when safe
  → buffer progress summaries locally
  → reconnect
  → sync latest state using attempt/fencing token
```

## Retry Policy

Retries must be bounded and explicit:

- max attempts per job/profile/task
- exponential backoff
- jitter
- retryable vs non-retryable error classification
- dead-letter after final failure

Suggested backoff:

```text
nextDelay = min(maxDelay, baseDelay * 2^attempt) + jitter
```

## Artifact Recovery

Before retrying after failure:

1. List staging artifacts.
2. Validate completed outputs if a final marker exists.
3. Remove incomplete temporary segments.
4. Preserve logs and structured error metadata.
5. Never publish partial output as completed.
