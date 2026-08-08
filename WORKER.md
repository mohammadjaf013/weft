# Worker

## Responsibilities

A worker executes assigned tasks safely and reports progress. It does not decide global scheduling.

## Execution Lifecycle

```text
receive assignment
  → validate lease/fencing token
  → create or recover local work directory
  → fetch/resolve input
  → ffprobe
  → prepare command
  → spawn process
  → stream progress
  → validate outputs
  → upload/finalize artifacts
  → report completion
  → cleanup local temporary files
```

## Local State

Workers should persist enough local state to survive process crashes or transient master/database outages:

- current assignment
- lease/fencing token
- attempt number
- input reference
- output staging path
- process PID when available
- last progress sample
- partial artifact inventory
- last report sync time

## FFmpeg Process Management

The worker must manage:

- command construction without shell injection
- stdin/stdout/stderr handling
- PID tracking
- process-group signaling
- graceful `SIGTERM`
- forced `SIGKILL` after timeout
- exit code mapping
- OOM detection where the platform exposes it
- disk-full detection
- broken-pipe detection
- timeout handling
- progress parsing from `-progress pipe:1` where possible

Do not rely only on human-oriented stderr parsing for progress.

## Graceful Shutdown

On shutdown:

1. Stop accepting new assignments.
2. Mark worker as draining.
3. Let safe tasks finish when within shutdown budget.
4. Send graceful termination to cancellable tasks.
5. Persist local state.
6. Release or let leases expire according to task safety.

## Idempotency

A worker may receive duplicate assignment or completion attempts. It must treat repeated operations as safe when lease and attempt metadata match.
