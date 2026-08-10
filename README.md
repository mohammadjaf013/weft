<div align="center">

<img src="docs/assets/weft-banner.png" alt="Weft" width="480">

# Weft

**A self-hosted media processing agent.** Feed it a video once — get the fully packaged output: H.264/HEVC HLS ladder, thumbnails, subtitles (incl. AI), master playlist, and upload to any storage.

**Durable · Crash-safe · Single static binary · No CGO · Event-sourced**

```
Weft — media processing agent
```

</div>

---

## Why Weft?

Media pipelines are fragile. Weft is designed as a **durable agent**: every state change and event is committed to a SQLite store **in one transaction**, so a crash mid-encode is recovered — not lost. It is not a wrapper around a script; it is a small distributed system you run on one box.

| Problem | Weft's answer |
|---|---|
| A long encode dies at 80% | Job is re-queued automatically when the worker lease expires; resume, don't restart |
| No visibility into what's happening | REST API + CLI + Prometheus metrics + HMAC-signed webhooks on every event |
| "It works on my machine" | `weft doctor` checks ffmpeg/ffprobe/config before you run anything |
| Post-production edits break a published video | `subtitle-add`, `dub-add` and `rebuild-master` update an existing HLS package in place |
| Subtitles in another language | `ai-subtitle` transcribes with **whisper.cpp** and translates/refines with **Gemini** (`hybrid`) |

---

## Features

- **DAG job pipelines** — profiles define task graphs (`vod-h264`, `vod-encode`, `audio`, `thumbnail`, `subtitle-add`, `dub-add`, `trim-update`, `poster-replace`, `ai-subtitle`); tasks run when all dependencies complete.
- **Durable event sourcing** — SQLite (WAL) store; every transition, event, and matching webhook outbox row commits atomically in one transaction.
- **Crash recovery** — workers lease tasks; expired leases are re-queued. No loss, no double-publish.
- **Priority queues** — `emergency → high → normal → low → background`, changeable on a queued job at runtime (`weft jobs priority`).
- **Smart concurrency** — worker pool auto-sizes to the host's CPU cores; an optional resource budget caps how much plugin-declared CPU/RAM cost can run at once, on top of measured-CPU admission gating.
- **Remote sources, not just remote destinations** — a job's input can be a relative path on a registered storage server or a direct `http(s)://` URL, fetched into a local cache automatically — not just local disk.
- **AI subtitles** — whisper.cpp (offline) and/or Gemini; `--provider whisper|gemini|hybrid`, `--src-lang` + `--lang` for transcription-then-translation.
- **Live progress** — whisper `-pp` and ffmpeg `-progress pipe:1` drive real progress events, surfaced via API and webhooks.
- **Live CLI dashboard** — `weft dashboard`: jobs, queue, workers, and host resources refreshed on an interval, with keyboard cancel/pause/resume/delete.
- **Webhooks** — at-least-once delivery, `X-Weft-Signature: HMAC-SHA256`, exponential backoff, dead-letter replay (`weft webhooks replay`).
- **Multi-storage** — local, SSH/SFTP, and S3 (hand-rolled SigV4, no SDK).
- **Master playlist recovery** — `POST /storage/rebuild-master` regenerates `playlist.m3u8` from whatever is actually on disk (recovers corrupted/old-binary masters).
- **Config export/import & cron** — back up/restore the running config; `cleanup`/`benchmark`/`health_scan` run on a schedule and are also triggerable by hand.
- **Security** — argon2id API keys with scopes; TLS supported.
- **Static binary** — pure Go, `modernc.org/sqlite` (no CGO), cross-compiled for linux/amd64 + linux/arm64.

---

## Quick start

```bash
# 1. Build (or download a release binary)
go build -o weft ./cmd/weft

# 2. Create a default config
./weft init-config

# 3. Check the environment
./weft doctor

# 4. Run the daemon
./weft serve --config weft.yaml
```

In another shell:

```bash
# Encode a video into HLS (360p..1080p) + thumbnails + subtitles + upload
weft jobs create /data/movie.mp4 --profile vod-h264

# Watch it
weft jobs get <job_id>
weft jobs list --status running

# AI subtitles: transcribe English audio, translate to Persian
weft jobs create /data/movie.mp4 --profile ai-subtitle --provider hybrid --src-lang en --lang fa

# Trim the clip before packaging (skip 50s from start, cut 10s off the end)
weft jobs create /data/movie.mp4 --profile vod-h264 --trim-start 50 --trim-end 10

# Custom thumbnails: exactly 5 evenly-spaced 1080x1080 stills instead of the
# default poster/sprite set; fetch one back as base64 from the API
weft jobs create /data/movie.mp4 --profile vod-h264 --thumb-count 5 --thumb-size 1080x1080
weft jobs get <job_id>            # lists produced assets (thumbnails included)
weft jobs asset <job_id> thumbnails/<base>_thumb_01.jpg
```

#### Trimming and thumbnails

`weft jobs create` accepts four extra flags:

| Flag | Meaning |
|---|---|
| `--trim-start <s>` | drop the first N seconds of the clip before HLS packaging (e.g. `50` starts at 50s) |
| `--trim-end <s>` | cut the last N seconds off the clip (e.g. `10` removes 10s from the end) |
| `--thumb-count <n>` | replace the default poster/sprite/stills with exactly N evenly-spaced thumbnails |
| `--thumb-size <w>x<h>\|original` | thumbnail dimensions, e.g. `1080x1080`, or `original` to keep source resolution (only used with `--thumb-count`) |

- Either or both of `--trim-start`/`--trim-end` may be set; both are applied to
  the `hls` **and** `thumbnail` tasks so the poster stills match the trimmed window.
- `--thumb-count` requires a profile that runs the `thumbnail` task (e.g.
  `vod-h264`, `vod-hevc`, `vod-encode`, `thumbnail`); thumbnails are uploaded
  to storage and listed under `assets` in `GET /jobs/{id}`.
- `weft jobs asset <job_id> <name>` returns the file as a base64 data URI via
  `GET /jobs/{id}/assets/{name}`, so you can fetch a thumbnail without touching
  the storage server directly.

---

## Architecture

```
┌─────────────┐     ┌──────────────────────────────────────────────┐
│  weft CLI   │────▶│  REST API (chi) · API keys · scopes          │
└─────────────┘     │  /jobs /queue /workers /webhooks /storage    │
                    └──────────────┬───────────────────────────────┘
                                   │ events (bus)
                    ┌──────────────▼───────────────────────────────┐
                    │  Scheduler (DAG + priority + lease expiry)   │
                    │  State Machine (legal transitions only)      │
                    └──────────────┬───────────────────────────────┘
                                   │ runnable tasks
                    ┌──────────────▼───────────────────────────────┐
                    │  Worker pool  ·  plugin registry (sandbox)   │
                    │  ffmpeg executor (-progress pipe:1)          │
                    └──────────────┬───────────────────────────────┘
                                   │ assets
                    ┌──────────────▼───────────────────────────────┐
                    │  Storage: local / SSH / S3  ·  upload plugin │
                    └──────────────────────────────────────────────┘

  Durable core:  SQLite (WAL) = store + event log + outbox (one txn)
```

### Layers

```
core/      Layer 1 — pure stdlib, zero I/O. Types, scheduler, state machine,
           event bus, lease store. Never touches disk/network.
runtime/   Layer 2 — implementations of core interfaces.
  store/sqlite    WAL SQLite store (durable event sourcing + outbox).
  executor/ffmpeg ffmpeg/ffprobe with `-progress pipe:1` parsing.
  registry/       plugin registry with panic sandbox.
  webhook/        HMAC-signed at-least-once delivery, backoff, dead letter.
  metrics/        Prometheus text export (hand-rolled, no deps).
  api/            chi REST API + argon2id API keys + scopes.
  worker/         worker loop (reserve → execute → mark done), resource budget.
  cron/           cleanup/benchmark/health_scan scheduler.
daemon/    The single assembly point (config → all components → Serve).
plugins/   Media + storage plugins (hls, subtitle, ai-subtitle, upload,
           poster_upload, ...).
profiles/  Profile → DAG templates (vod-h264, audio, ai-subtitle,
           trim-update, poster-replace, ...).
configs/   weft.yaml schema + startup validation.
cli/       Thin CLI over the API (serve, doctor, jobs, keys, dashboard, ...).
e2e/       Full lifecycle test over the real daemon (fake ffmpeg).
chaos/     Failure injection: plugin panic, lease expiry, webhook retry.
```

### Job lifecycle

```
queued → reserved → running → uploading → completed
                       ↕ paused/resumed
             └──────── (any step fails) ───────▶ failed
```

- A task becomes runnable the moment all DAG dependencies are done.
- Every state change + its event + any matching webhook outbox row commits in **one transaction**.
- Workers lease tasks; a crashed worker's task is **re-queued** when the lease expires.
- A resumed job behaves exactly like a running one for every later transition — pausing is not a dead end.

---

## Configuration

`weft init-config` writes a full `weft.yaml` with defaults. Everything validates at startup — a broken provider refuses to boot.

```yaml
network:
  listen: 127.0.0.1:8443
security:
  api_keys: true
  admin_api_key: "<set this>"
ai_subtitle:
  provider: whisper            # whisper | gemini | hybrid
  whisper:
    model_path: "/opt/weft/models/ggml-medium.bin"
    language: "en"             # source language of the audio
    threads: 8
    temperature: 0.0           # deterministic output
    prompt: "Spider-Man, Peter Parker, Zendaya"
  gemini:
    api_key: ""                # required for hybrid translation
    model: "gemini-1.5-flash"
    language: "fa"             # target language
```

See [docs/SETUP-FA.md](docs/SETUP-FA.md) for the full configuration reference and deployment guide.

---

## CLI & REST API

Every CLI command is a thin client over a matching REST endpoint — nothing
is CLI-only. The most-used ones:

```
weft serve --config <path>             run the agent daemon
weft doctor                            check ffmpeg, config, db, connectivity
weft jobs create <input_ref> --profile <name> [flags...]
weft jobs list | get <id> | action <id> <cancel|retry|pause|resume> | delete <id>
weft dashboard                         live TUI: jobs/queue/workers/system, select+act
weft queue | workers | system | benchmark
weft webhooks create <url> <event...> | keys create <name> <scope...>
weft storage add <id> <type> ...       register a destination/source server
weft config export/import | cron list/run
```

`weft jobs create` alone takes ~20 flags (priority, destination/source
server, trim window, custom thumbnails, subtitle language/provider, forced/
default track flags, ...); the REST surface mirrors it 1:1, scoped by API
key (`jobs:write`, `storage:manage`, `config:manage`, ...). All remote
commands accept `--api <url>` and `--key <token>`.

**Full CLI + REST reference — every flag, every endpoint, request/response
shapes, worked examples:** [docs/REFERENCE.md](docs/REFERENCE.md) (English) ·
[docs/CLI-API-FA.md](docs/CLI-API-FA.md) (Persian).

---

## Webhooks

Every domain event is fanned out to the outbox and delivered **at-least-once**:

```
X-Weft-Event: job.completed
X-Weft-Signature: HMAC-SHA256(secret, body)
```

Wire events include `job.created`, `job.started`, `job.progress`, `task.progress`, `job.completed`, `job.failed` and more. Failed deliveries retry with exponential backoff and land in a replayable dead-letter log.

---

## Development

```bash
go build ./...      # build everything
go vet ./...        # static checks
go test ./...       # unit + integration + chaos (integration skips without ffmpeg)
```

Cross-compile for Linux:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o dist/weft-linux-amd64 ./cmd/weft
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o dist/weft-linux-arm64 ./cmd/weft
```

> No CGO: SQLite is `modernc.org/sqlite` (pure Go), S3 is hand-rolled SigV4 — the daemon is one static binary.

---

## Vision

Weft is a **self-hosted media processing agent** evolving from an early open-source project into a production-grade orchestration platform for multimedia workloads. Our long-term vision is simple: **make multimedia infrastructure as programmable, scalable, and reliable as modern cloud infrastructure.**

We want Weft to become the infrastructure layer where companies can run video, audio, streaming, and AI media workloads without having to build their own orchestration platform from scratch.

---

## Roadmap

Weft today is a **single-node** agent: DAG job pipelines, crash recovery,
priority queues + a resource-aware worker pool (CPU/RAM), remote sources,
whisper/Gemini AI subtitles, webhooks, a live CLI dashboard, cron
maintenance jobs, and config export/import — all shipped, not planned. The
roadmap is what's genuinely still ahead, mostly things that only make sense
once there's more than one node or more than one media capability class:

- **Distributed / cluster orchestration** — scheduling and execution across
  *multiple* Weft nodes (today's resource-aware scheduling is per-node
  only); node discovery, failover, and workload redistribution across a
  cluster.
- **GPU-aware scheduling** — GPU as a schedulable resource, alongside the
  existing CPU/RAM budget, for GPU-accelerated encode/AI workloads.
- **Job-to-job pipelines** — chaining complete jobs together with
  dependencies (distinct from today's DAG, which sequences *tasks within
  one job*).
- **AI subtitle quality** — subtitle sync correction and multi-provider
  quality comparison, beyond today's transcribe-then-translate.
- **Broader AI media processing** — speech detection, audio enhancement,
  scene analysis, metadata generation, content enrichment.
- **DASH packaging and HLS/DASH encryption** (AES-128, Sample-AES, DRM) —
  today's HLS output is unencrypted.
- **More storage backends** — GCS, Azure Blob, and others beyond today's
  local/SSH/S3.

Development is funded through subscriptions, which help fund development
infrastructure, multi-server and GPU testing environments, AI processing
costs, real-world workload testing, documentation, and developer tooling.

---

## Documentation

- [docs/README.md](docs/README.md) — architecture overview
- [docs/REFERENCE.md](docs/REFERENCE.md) — complete reference manual (config, CLI, API, webhooks, AI subtitles)
- [docs/SETUP-FA.md](docs/SETUP-FA.md) — setup, configuration, deployment
- [docs/GUIDE-FA.md](docs/GUIDE-FA.md) — user guide
- [docs/CLI-API-FA.md](docs/CLI-API-FA.md) — CLI + API reference

---

## License

[MIT](LICENSE) © Mohammad Jaf
