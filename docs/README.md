# Weft

Media-processing agent: ingest a video once, get the full packaged output
(ladder, thumbnails, subtitles, HLS + master playlist, upload) with durable
event sourcing, webhooks, and crash recovery.

## Layout

```
core/      Layer 1 — pure stdlib, zero I/O. Types, interfaces, scheduler,
           state machine, event bus, lease store. Never touches disk/network.
runtime/   Layer 2 — implementations of core interfaces.
  store/sqlite    WAL SQLite store (durable event sourcing, outbox).
  scheduler/      (via core) DAG + priority.
  executor/ffmpeg ffmpeg/ffprobe with `-progress pipe:1` parsing.
  registry/       plugin registry + panic sandbox.
  webhook/        HMAC-signed at-least-once delivery, backoff, dead letter.
  metrics/        hand-rolled Prometheus text export.
  api/            chi REST API + argon2id API keys + scopes.
  worker/         worker loop (reserve → execute → mark done).
daemon/    The single assembly point (Config → all components → Serve).
plugins/   Media + storage plugins.
profiles/  Profile → DAG templates (vod-h264, audio, thumbnail, ai-subtitle).
configs/   weft.yaml schema + startup validation.
cli/       Thin CLI over the API (`serve`, `version`, `init-config`).
e2e/       Full lifecycle test over the real daemon (fake ffmpeg).
chaos/     Failure injection: plugin panic, lease expiry, webhook retry.
cmd/weft/  main.
```


## Master specification

The Codex-facing project specification lives at the repository root:

- `AGENTS.md` — agent instructions, boundaries, and definition of done.
- `ARCHITECTURE.md` — current and target architecture.
- `REQUIREMENTS.md` — functional, non-functional, and distributed requirements.
- `JOB_LIFECYCLE.md` — target state machine, transition authority, and invariants.
- `SCHEDULER.md` — DAG, priority, eligibility, and worker scoring rules.
- `RESOURCE_MANAGER.md` — worker heartbeat payloads, health states, and metrics.
- `WORKER.md` — worker execution, local state, FFmpeg process management, and shutdown.
- `RECOVERY.md` — worker failure, master restart, DB outage, retry, and artifact recovery.
- `DATABASE.md` — persistence entities, transaction rules, migrations, and outage target.
- `API.md` — target REST API contract.
- `CLI.md` — current CLI compatibility and target distributed commands.
- `SECURITY.md` — credentials, path safety, API keys, webhooks, and worker security.
- `CONFIGURATION.md` — configuration areas and path rules.
- `TESTING.md` — required checks and failure cases.
- `DEPLOYMENT.md` — single-daemon and target distributed deployment guidance.
- `DEVELOPMENT.md` — development workflow and code organization.
- `ROADMAP.md` — phased path from the current daemon to distributed orchestration.

## Build & test

```
go build ./...
go vet ./...
go test ./... -count=1
```

No CGO: SQLite is `modernc.org/sqlite` (pure Go), S3 is hand-rolled SigV4,
so the daemon is a single static binary.

## Run

```
weft init-config            # writes ./weft.yaml (defaults)
weft serve --config weft.yaml
```

Validated config refuses to start with a broken provider (whisper without
model_path, gemini without api_key). API keys default on; create keys via
`POST /keys` with the admin key from config.

## Job lifecycle

`queued → reserved → running → uploading → completed` (legal transitions
enforced by the state machine). A task becomes runnable the moment all its
DAG dependencies are done. Every state change + its Event commit in ONE
transaction. Workers lease tasks; a crashed worker's task is requeued when
its lease expires.

## Webhooks

Every event is fanned out to the outbox, delivered at-least-once with
`X-Weft-Signature: HMAC-SHA256(secret, body)`, exponential backoff, and a
replayable dead-letter log. Wire names (`job.completed`, …) live only in
`runtime/webhook/dispatcher.go`.
