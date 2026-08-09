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
  worker/         worker loop (reserve → execute → mark done), resource budget.
  cron/           cleanup/benchmark/health_scan scheduler.
daemon/    The single assembly point (Config → all components → Serve).
plugins/   Media + storage plugins (incl. poster_upload).
profiles/  Profile → DAG templates (vod-h264, audio, thumbnail, ai-subtitle,
           trim-update, poster-replace).
configs/   weft.yaml schema + startup validation.
cli/       Thin CLI over the API (`serve`, `version`, `init-config`, `dashboard`, ...).
e2e/       Full lifecycle test over the real daemon (fake ffmpeg).
chaos/     Failure injection: plugin panic, lease expiry, webhook retry.
cmd/weft/  main.
```

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

`queued → reserved → running → uploading → completed`, with `paused`/`resumed`
branching off `running` and behaving identically to it afterward (legal
transitions enforced by the state machine — see `core/statemachine.go`'s
`allowedTransitions`). A task becomes runnable the moment all its DAG
dependencies are done. Every state change + its Event + any matching webhook
outbox row commit in ONE transaction. Workers lease tasks; a crashed worker's
task is requeued when its lease expires.

## Webhooks

Every event is enqueued into the outbox in the SAME transaction as the state
change that triggered it (see `runtime/store/sqlite/store.go`'s
`insertEventTx`/`enqueueOutboxTx` — not a separate async step), then
delivered at-least-once with `X-Weft-Signature: HMAC-SHA256(secret, body)`,
exponential backoff, and a replayable dead-letter log
(`weft webhooks replay <event_id>`). The wire-name mapping
(`job.completed`, …) lives in `core/wirename.go` — shared by
`runtime/webhook` and `runtime/store/sqlite`, which is why it's in `core`
rather than either of them.
