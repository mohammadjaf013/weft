# Roadmap

## Phase 0 — Document Baseline

- Add Codex-facing `AGENTS.md`.
- Add architecture, requirements, lifecycle, scheduler, worker, recovery, API, CLI, database, security, configuration, testing, deployment, and development docs.
- Keep current code behavior unchanged.

## Phase 1 — Close Specification Gaps In Single-Daemon Mode

- Add structured job/task error metadata.
- Add retry attempt metadata and retry backoff state.
- Make preparing/finalizing phases explicit where useful.
- Improve scheduler tests around priority and starvation.
- Expand metrics for jobs, tasks, FFmpeg, scheduler, and storage.

## Phase 2 — Worker Registry And Heartbeats

- Persist worker records.
- Add worker registration API.
- Add heartbeat API and resource snapshots.
- Add health-state derivation.
- Add worker drain support.

## Phase 3 — Resource-Aware Scheduling

- Add worker eligibility checks.
- Add scoring weights in configuration.
- Add CPU/memory/disk/active-job scoring.
- Add worker capability matching.
- Add scheduler metrics and tests.

## Phase 4 — Distributed Worker Agent

- Add worker process mode.
- Add assignment polling or push channel.
- Add local state cache.
- Add progress/completion report APIs.
- Add lease/fencing-token enforcement.

## Phase 5 — Recovery Hardening

- Add orphaned task detection.
- Add artifact verification.
- Add incomplete output cleanup.
- Add master restart reconciliation.
- Add DB outage/local sync behavior for workers.

## Phase 6 — Production Operations

- Add deployment examples.
- Add backup/restore docs.
- Add dashboard-ready metrics.
- Add security hardening checklist.
- Add migration guides for distributed mode.
