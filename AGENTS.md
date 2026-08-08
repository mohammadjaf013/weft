# AGENTS.md

## Project

Weft is a production-grade, business-agnostic media conversion and orchestration system.

The system is responsible for:

- media conversion
- HLS generation
- AES-128 HLS encryption
- subtitle conversion and AI-assisted subtitle workflows
- thumbnail generation
- durable job scheduling
- worker resource monitoring
- job retry and recovery
- progress tracking
- multi-storage delivery
- multi-server orchestration as the target architecture evolves

The system MUST NOT know about business entities such as:

- Movie
- Series
- Episode
- User
- Subscription
- Payment
- Catalog

The system only knows about generic media jobs, files, tasks, profiles, workers, storage targets, events, and artifacts.

## Core Architecture

Target architecture:

```text
Master
  ├─ API
  ├─ CLI client interface
  ├─ Job Manager
  ├─ Scheduler
  ├─ Resource Manager
  ├─ Worker Registry
  ├─ State Machine
  ├─ Database / durable event store
  └─ Outbox / delivery mechanisms

Worker Agent
  ├─ Local state cache
  ├─ FFmpeg / FFprobe executor
  ├─ Plugin runtime
  ├─ Progress reporter
  ├─ Artifact staging
  └─ Cleanup / recovery hooks
```

Current architecture is a single daemon with an internal worker pool. Preserve existing behavior while evolving toward explicit master/worker boundaries.

Master decides WHERE a job or task runs.
Worker decides HOW the assigned work executes.

## Repository Inspection Rule

Before modifying code:

1. Inspect the repository structure.
2. Inspect existing implementations before creating replacements.
3. Inspect tests near the touched code.
4. Inspect configuration and migrations when behavior changes persistence or startup.
5. Inspect API, CLI, and docs when changing user-facing behavior.
6. Never rewrite an existing subsystem without understanding it first.
7. Never create duplicate abstractions when an existing abstraction can be extended safely.
8. Prefer small, reviewable, phase-by-phase changes.

## Non-Negotiable Rules

1. Never couple Weft to a specific business application.
2. Never assume a worker has a fixed CPU count.
3. Never assume a fixed number of concurrent jobs.
4. Never lose job state because the database temporarily disappears.
5. Jobs and tasks must be recoverable after process crashes.
6. Job and task operations must be idempotent where retries are possible.
7. Every state transition must be explicit and validated.
8. Every worker must report resource state in distributed mode.
9. Scheduling must consider priority, CPU, memory, disk, active jobs, worker health, and compatibility.
10. Failed jobs must have structured error information.
11. Retries must use bounded exponential backoff with jitter.
12. Configuration must be environment-driven or config-file driven.
13. No hardcoded production paths.
14. No hardcoded server addresses.
15. No hardcoded credentials.
16. Public API errors must be structured and stable.
17. Graceful shutdown must be supported for master and worker processes.
18. Documentation must be updated with behavior changes.

## Required Reading Order for Agents

When starting implementation work, read these files first:

1. `AGENTS.md`
2. `ARCHITECTURE.md`
3. `REQUIREMENTS.md`
4. `JOB_LIFECYCLE.md`
5. `SCHEDULER.md`
6. `WORKER.md`
7. `RESOURCE_MANAGER.md`
8. `RECOVERY.md`
9. `API.md`
10. `CLI.md`
11. `DATABASE.md`
12. `CONFIGURATION.md`
13. `SECURITY.md`
14. `TESTING.md`
15. `ROADMAP.md`

## Definition of Done

A feature is NOT complete unless:

- implementation exists
- unit tests exist
- integration tests exist where required
- failure cases are handled
- retry behavior is implemented where applicable
- logging exists for operationally important behavior
- metrics exist where appropriate
- configuration is documented
- API contract is documented when changed
- migrations exist for persistence changes
- Docker/deployment impact is considered
- graceful shutdown is implemented or unaffected
- crash recovery is tested or explicitly documented as not applicable
- race conditions are considered
- idempotency is verified where retries can occur
- documentation is updated

## Current Known Gap

The current repository already contains a durable single-daemon media-processing system. The target documented here is stricter and more distributed than the current implementation. Prefer incremental evolution over large rewrites.
