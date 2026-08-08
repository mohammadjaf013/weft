# Architecture

## Purpose

Weft is a media conversion and orchestration system. It accepts generic media jobs, expands them into a task graph, schedules runnable work, executes media tooling, records progress, stores artifacts, and exposes status through API, CLI, metrics, and events.

Weft must remain business-agnostic. Integrating applications may map their own domain entities to Weft job metadata externally, but Weft itself must not model those entities.

## Current Architecture

The current implementation is a single daemon with an internal worker pool:

```text
CLI / HTTP clients
       │
       ▼
REST API
       │
       ▼
DAG Scheduler ── State Machine
       │              │
       ▼              ▼
Worker Pool ─── Durable Store / Event Log
       │
       ▼
Plugins / FFmpeg / Storage
```

Key properties:

- Jobs are expanded into DAG tasks.
- Legal status transitions are enforced by the state machine.
- Task leases allow crashed workers to be detected and work to be requeued.
- SQLite/WAL provides durable local persistence.
- CLI calls the REST API and should remain thin.

## Target Architecture

The target production architecture separates the control plane from execution:

```text
                 ┌─────────────────────────────┐
                 │            Master           │
                 │ API / Job Manager / State    │
Client / CLI ───▶│ Scheduler / Resource Manager │
                 │ Worker Registry / Store      │
                 └─────────────┬───────────────┘
                               │ assignments
          ┌────────────────────┼────────────────────┐
          ▼                    ▼                    ▼
   ┌─────────────┐      ┌─────────────┐      ┌─────────────┐
   │ Worker A    │      │ Worker B    │      │ Worker C    │
   │ FFmpeg      │      │ FFmpeg      │      │ FFmpeg      │
   │ Local State │      │ Local State │      │ Local State │
   └─────────────┘      └─────────────┘      └─────────────┘
```

The master owns:

- API contracts
- job creation and validation
- state-machine authority
- worker registry
- resource-aware scheduling
- retry policy
- recovery coordination
- metrics aggregation

Workers own:

- local process execution
- FFmpeg/FFprobe lifecycle
- local staging paths
- local progress capture
- best-effort local state during transient master/database outages
- cleanup of local temporary artifacts

## Boundary Rules

- Master decides where work runs.
- Worker decides how assigned work executes.
- Plugins must not bypass the state machine.
- Storage plugins must not expose credentials in API responses or logs.
- The API layer must not embed business-domain behavior.
- CLI commands must be thin wrappers around API calls.

## Data Flow

```text
POST /jobs
  → validate request
  → create Job
  → expand Profile to DAG Tasks
  → persist JobCreated event
  → scheduler finds runnable tasks
  → scheduler scores workers
  → task assignment / lease
  → worker executes
  → worker reports progress
  → task completion unlocks dependents
  → output validation
  → upload/finalize
  → completed event and webhooks
```

## Failure Model

Weft assumes failures are normal:

- master process crashes
- worker process crashes
- FFmpeg exits unexpectedly
- disk fills during staging
- network upload fails
- database becomes temporarily unavailable
- webhook delivery fails
- duplicate requests are retried by clients

Design features must be recoverable, observable, and idempotent under these failures.
