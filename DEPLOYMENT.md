# Deployment

## Local Single-Daemon Mode

Single-daemon mode runs API, scheduler, store, and workers in one process. This mode must remain supported for small installations and development.

## Target Distributed Mode

Distributed mode runs one or more masters and independent worker agents. Initial distributed support may use a single active master with many workers before introducing HA master behavior.

## Operational Requirements

- persistent database volume
- persistent or recoverable staging directories
- FFmpeg and FFprobe installed or packaged
- configured storage destinations
- metrics scraping
- log collection
- secret management
- backup and restore procedure for the database

## Rollout Guidance

1. Start with single-daemon compatibility.
2. Add worker registration and heartbeat.
3. Add resource-aware scheduling.
4. Add worker agents.
5. Add recovery reconciliation.
6. Add HA controls only after single-master distributed mode is stable.
