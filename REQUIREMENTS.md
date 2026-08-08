# Requirements

## Functional Requirements

1. Accept generic media jobs through REST API and CLI.
2. Expand jobs into profile-defined DAG tasks.
3. Convert video and audio using FFmpeg.
4. Generate HLS outputs and master playlists.
5. Support AES-128 HLS encryption when requested by profile/options.
6. Generate thumbnails.
7. Convert, add, or generate subtitles where supported by plugins.
8. Upload or place artifacts into configured storage destinations.
9. Track job and task progress.
10. Expose job, queue, worker, profile, plugin, storage, health, and metrics endpoints.
11. Support cancel, retry, pause, and resume actions.
12. Deliver webhooks at least once with retry and replay support.
13. Provide a CLI for all primary API workflows.

## Non-Functional Requirements

1. Durable persistence for state transitions and emitted events.
2. Explicit state machine for job transitions.
3. Idempotent job/task operations where retries are possible.
4. Bounded exponential retry with jitter.
5. Structured errors for API responses and failed jobs.
6. Configurable paths, credentials, endpoints, limits, and timeouts.
7. Graceful shutdown for master and worker processes.
8. Metrics for jobs, tasks, workers, scheduler, FFmpeg, storage, and webhooks.
9. No business-domain coupling.
10. No hardcoded production paths or credentials.

## Distributed Requirements

These are target requirements for the multi-server evolution:

1. Workers can run as independent agents on different machines.
2. Workers register with the master.
3. Workers heartbeat with resource snapshots.
4. Master marks workers as healthy, busy, degraded, draining, overloaded, or offline.
5. Scheduler scores eligible workers before assignment.
6. Running tasks on offline workers become orphaned and enter recovery.
7. Workers keep local execution state during transient master/database outages.
8. Master reconciles jobs, tasks, workers, and artifacts after restart.

## Compatibility Requirements

1. Preserve existing CLI and API behavior unless a migration note is documented.
2. Preserve existing profile names where possible.
3. Existing single-daemon mode remains supported while distributed mode is introduced.
4. Existing tests must continue to pass.
