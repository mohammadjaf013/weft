# Weft Reference Manual

Version 0.1.0. The complete reference for configuration, CLI commands, the
REST API, profiles, storage, webhooks, and the AI-subtitle pipeline.

> Fast answers: **Quick start** → [Install & run](#1-install--run) ·
> **Config** → [Configuration](#2-configuration) · **CLI** →
> [CLI reference](#3-cli-reference) · **API** → [REST API](#4-rest-api) ·
> **Webhooks** → [Webhooks](#5-webhooks) · **AI subtitles** →
> [AI subtitles](#6-ai-subtitles)

---

## 1. Install & run

Weft is a single static binary (pure Go, no CGO). Build from source or copy a
release binary to the target machine.

```bash
# build
go build -o weft ./cmd/weft

# cross-compile for Linux servers
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o dist/weft-linux-amd64 ./cmd/weft
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o dist/weft-linux-arm64 ./cmd/weft
```

```bash
# 1. write a default weft.yaml (fails if the file already exists)
./weft init-config

# 2. verify the environment (ffmpeg, ffprobe, config, db, storage, network)
./weft doctor

# 3. run the daemon
./weft serve --config weft.yaml
```

`serve` validates the config first and **refuses to start** in a broken state
(e.g. whisper provider without a readable `model_path`, or gemini without
`api_key`).

The daemon creates `weft.db` (SQLite, WAL) and the `data/` / `work/` folders in
its working directory. Every other CLI command is a thin HTTP client that reads
`network.listen` and `security.admin_api_key` from `weft.yaml` automatically.

---

## 2. Configuration

Full schema for `weft.yaml`. All fields are optional — the defaults below apply
when omitted.

```yaml
scheduler:
  mode: "dynamic"              # concurrency mode
  max_cpu_percent: 85          # don't schedule new work above this CPU
  max_load_average: 0          # optional load-average ceiling
  max_estimated_cpu_cores: 0   # 0 = unlimited; caps the SUM of in-flight
                                # tasks' DECLARED plugin cost (not measured
                                # usage — complements max_cpu_percent, which
                                # reacts to actual saturation)
  max_estimated_ram_mb: 0      # same idea, for declared RAM cost

workers:
  min: 1                       # always at least this many workers
  max: 0                       # 0 = auto: one worker per logical CPU core
                                # (bounded below by min)
  lease_ttl_seconds: 300       # crash recovery: task re-queued after this

queue:
  priorities: [emergency, high, normal, low, background]  # in this order

workflow:
  default_timeout_seconds: 3600   # per-task timeout

plugins:
  enabled:                       # the default set
    - ffmpeg-video
    - ffmpeg-audio
    - subtitle
    - thumbnail
    - hls
    - master_playlist
    - master_update
    - upload
    - poster_upload
    - storage-local
    - storage-ssh
    - storage-s3
    - ai-subtitle

network:
  listen: "127.0.0.1:8443"     # use "0.0.0.0:8443" to expose
  tls: "off"                   # "off" (TLS not yet implemented)

security:
  api_keys: true               # false disables auth (dangerous off-LAN)
  key_hash: "argon2id"         # hashing for stored keys
  webhook_signing: "hmac-sha256"
  admin_api_key: "CHANGE-ME"   # default admin token (used by CLI)

database:
  path: "weft.db"

executor:
  ffmpeg_path: "ffmpeg"
  ffprobe_path: "ffprobe"

storage:
  local:
    base_path: "./data"        # destination 0 = this directory

ai_subtitle:
  provider: "whisper"          # whisper | gemini | hybrid (server default)
  whisper:
    model_path: "/opt/weft/models/ggml-medium.bin"  # required for whisper
    language: "en"             # source language of the audio (whisper -l)
    threads: 8                 # -t CPU threads
    temperature: 0.0           # --temperature; 0.0 = deterministic output
    prompt: "Spider-Man, Peter Parker, Zendaya, Green Goblin, Hulk, Scorpion"
    bin_path: ""               # default "whisper-cli" (PATH) or absolute
  gemini:
    api_key: ""                # or env GEMINI_API_KEY; needed for hybrid
    model: "gemini-1.5-flash"
    language: "fa"             # default target language
  auto_generate:
    enabled: true              # insert ai_subtitle when a video has no subs
    only_if_missing: true
    target_languages: [fa, en]

cron:
  cleanup:
    schedule: "0 3 * * *"      # daily 03:00
    retention_hours: 72        # how long to keep work dirs
    event_retention_days: 30   # how long to keep the event log
  benchmark:
    schedule: "0 4 * * 0"      # weekly Sunday 04:00
  health_scan:
    schedule: "*/5 * * * *"    # every 5 minutes

# optional webhooks declared in config (also creatable via API/CLI)
webhooks:
  - id: wh-1
    url: "https://example.com/hook"
    secret: "s3cret"
    events: ["job.completed", "job.failed"]
    max_retries: 8
    timeout_seconds: 10
```

### Config validation rules

- `ai_subtitle.provider=whisper` → `whisper.model_path` must exist and be a file.
- `ai_subtitle.provider=gemini` → `gemini.api_key` must be set (or `GEMINI_API_KEY` env).
- Unknown provider → startup error. Empty provider → ai-subtitle simply disabled.
- A broken provider makes `weft serve` and `weft doctor` fail loudly.

---

## 3. CLI reference

```
weft serve [--config <path>]           run the agent daemon
weft init-config [<path>]              write a default weft.yaml
weft doctor [--config <path>]          environment check (ffmpeg, config, db, ...)
weft version                           print version (also -v / --version)

weft jobs list [--status S] [--priority P] [--limit N]
weft jobs get <id>
weft jobs create <input_ref> --profile <name> [flags — see below]
weft jobs events <id>
weft jobs log <job_id> <task_id>
weft jobs asset <job_id> <asset_name>
weft jobs action <id> <cancel|retry|pause|resume>
weft jobs priority <id> <emergency|high|normal|low|background>
weft jobs delete <id> [?purge_files=false via the API — CLI always purges]

weft keys create <name> <scope...> | keys list | keys delete <id>
weft webhooks create <url> <event...> [--secret S] | list | delete <id> | replay <event_id>
weft storage list | add <id> <type> [...] | rebuild --path <dir>

weft queue | workers [scale <n>] | profiles | plugins | metrics | system
weft benchmark [run] | benchmark get
weft config export [--out F] [--include-secrets] | config import <file>
weft cron list | cron run <cleanup|benchmark|health_scan>
weft dashboard [--interval 2s]
```

Global flags (may appear before or after the subcommand): `--api <url>`,
`--key <token>`, `--config <path>`.

### `jobs create` options

| Flag | Meaning |
|---|---|
| `--profile <name>` | Required. Profile/DAG template — see [§7 Profiles](#7-profiles). |
| `--priority <p>` | `emergency` \| `high` \| `normal` \| `low` \| `background` (default `normal`) |
| `--destination <n>` | Destination storage server id; `0` = local (default). |
| `--source-server <n>` | When set, `input_ref` is a relative path resolved against this REGISTERED storage server (same servers `weft storage add` registers) instead of a local filesystem path — lets trim/thumbnail/subtitle operate on a source that lives on ssh/s3, not just local disk. `input_ref` can also be a plain `http://`/`https://` URL without this flag; it's fetched directly. |
| `--lang <L>` | Subtitle target language (e.g. `fa`, `en`). |
| `--src-lang <L>` | Language the audio is spoken in (whisper `-l`). Differs from `--lang` → hybrid translates. |
| `--provider <p>` | Per-job ai-subtitle provider: `whisper` \| `gemini` \| `hybrid` (empty = server default). |
| `--name <n>` | Base name for output assets (re-running replaces the same track; also used by `trim-update`/`poster-replace` to target the existing published files). |
| `--path <p>` | Subdirectory under the destination root (e.g. `movie`, `series`). |
| `--delete-source` | Delete the source file after a successful upload. |
| `--trim-start <s>` | Skip this many seconds from the start of the clip before HLS packaging. |
| `--trim-end <s>` | Cut this many seconds off the end of the clip. Either or both of trim-start/trim-end may be set. Also used by the `trim-update` profile to re-trim an already-published video in place. |
| `--thumb-count <n>` | Produce exactly N evenly-spaced thumbnails instead of the default poster/sprite/stills set. |
| `--thumb-at <s>` | Capture a single frame at this offset (seconds) instead of the default set or `--thumb-count`'s N-evenly-spaced mode — mutually exclusive with `--thumb-count`. |
| `--thumb-size <WxH\|original>` | Custom thumbnail dimensions; applies to `--thumb-count` or `--thumb-at`. |
| `--forced` | Mark this subtitle track `FORCED=YES` (`--profile subtitle-add`; ignored otherwise). |
| `--default` | Mark this subtitle track `DEFAULT=YES` (`--profile subtitle-add`; ignored otherwise). |

### `storage add` options

```
weft storage add <id> <type> [flags]
```

Types: `ssh` | `local` | `s3` | `minio` | `r2`.

| Flag | For | Meaning |
|---|---|---|
| `--host <h>` | ssh/s3 | Host or endpoint |
| `--user <u>` | ssh | SSH user (default `weft`) |
| `--key-path <p>` | ssh | Private key path (auth method 1) |
| `--password <p>` | ssh | Password (auth method 2) |
| `--port <n>` | ssh | Port (default 22) |
| `--base-path <p>` | all | Root directory on the destination |
| `--bucket <b>` | s3/minio/r2 | Bucket name |
| `--region <r>` | s3/minio/r2 | Region (default `us-east-1`) |
| `--access-key <k>` | s3/minio/r2 | Access key |
| `--secret-key <k>` | s3/minio/r2 | Secret key |

### `doctor` checks

`weft doctor` prints one block per check and exits non-zero when any check
fails (scriptable). Checks: binary version · config validity · ffmpeg ·
ffprobe · AI subtitle provider · database open + migrate · storage writable ·
listen port free · plugin names valid.

---

## 4. REST API

Base: `http://<listen>/`. Auth: `Authorization: Bearer <api_key>`. Every error
has the shape `{"error":{"code":"...","message":"..."}}`.

### Endpoints

| Method | Path | Scope |
|---|---|---|
| GET | `/health` | — |
| GET | `/` | service identity: version, profiles, plugins, endpoints |
| GET | `/jobs` | `jobs:read` |
| POST | `/jobs` | `jobs:write` |
| GET | `/jobs/{id}` | `jobs:read` |
| GET | `/jobs/{id}/events` | `jobs:read` |
| GET | `/jobs/{id}/tasks/{taskID}/log` | `jobs:read` |
| GET | `/jobs/{id}/assets/{name}` | `jobs:read` |
| DELETE | `/jobs/{id}` | `jobs:write` |
| PATCH | `/jobs/{id}/priority` | `jobs:write` |
| POST | `/jobs/{id}/{action}` | `jobs:write` |
| GET | `/queue` | `queue:read` |
| GET | `/workers` | `workers:read` |
| POST | `/workers/scale` | `workers:write` |
| GET | `/storage/servers` | `storage:manage` |
| POST | `/storage/servers` | `storage:manage` |
| POST | `/storage/rebuild-master` | `storage:manage` |
| GET | `/webhooks` | `webhooks:manage` |
| POST | `/webhooks` | `webhooks:manage` |
| DELETE | `/webhooks/{id}` | `webhooks:manage` |
| POST | `/webhooks/{event_id}/replay` | `webhooks:manage` |
| GET | `/keys` | `keys:manage` |
| POST | `/keys` | `keys:manage` |
| DELETE | `/keys/{id}` | `keys:manage` |
| GET | `/profiles` | `profiles:read` |
| GET | `/plugins` | `plugins:read` |
| POST | `/benchmark` | `metrics:read` |
| GET | `/benchmark` | `metrics:read` |
| GET | `/metrics` | `metrics:read` |
| GET | `/system` | `metrics:read` |
| GET | `/config/export` | `config:manage` |
| POST | `/config/import` | `config:manage` |
| GET | `/cron` | `cron:manage` |
| POST | `/cron/{job}/run` | `cron:manage` |

### Scopes

`jobs:read` · `jobs:write` · `queue:read` · `workers:read` · `workers:write` ·
`storage:manage` · `webhooks:manage` · `keys:manage` · `profiles:read` ·
`plugins:read` · `metrics:read` · `config:manage` · `cron:manage`

### `POST /jobs` — create a job

```json
{
  "input_ref": "/srv/weft/film.mp4",
  "profile": "vod-h264",
  "destination_id": 0,
  "source_server_id": 0,
  "priority": "normal",
  "lang": "fa",
  "src_lang": "en",
  "name": "movie",
  "path": "Series-Test/movie1",
  "provider": "hybrid",
  "delete_source": false,
  "trim_start": 0,
  "trim_end": 0,
  "thumb_count": 0,
  "thumb_at": 0,
  "thumb_size": "",
  "forced": false,
  "default": false
}
```

`input_ref` can be a local path, an `http://`/`https://` URL (fetched
directly), or — when `source_server_id` is set — a relative path resolved
against that registered storage server (local/ssh/s3, same servers `POST
/storage/servers` registers). This is what lets trim/thumbnail/subtitle
profiles run against a source that lives on remote storage, not just local
disk; the fetched file is cached under a per-job scratch dir and cleaned up
once the job finishes.

Response `201 Created`:

```json
{ "id": "job_...", "status": "queued", "tasks": ["task_...", "..."] }
```

Validation errors (`400`): `invalid_request` (malformed / missing fields),
`unknown_profile`, `invalid_priority`, `unknown_destination`,
`unknown_source_server`, `invalid_provider`.

### `GET /jobs/{id}` — job detail

```json
{
  "id": "job_...",
  "status": "running",
  "priority": "normal",
  "profile": "vod-h264",
  "input_ref": "/srv/weft/film.mp4",
  "destination_id": 0,
  "dest_path": "Series-Test/movie1",
  "source_server_id": 0,
  "verified": false,
  "overall_progress": 42.5,
  "error": "",
  "tasks": [
    { "id": "task_...", "kind": "hls", "status": "running",
      "progress_percent": 61.2, "depends_on": [], "error": "",
      "started_at": "2026-01-01T12:00:00Z" }
  ],
  "assets": [ { "kind": "thumbnail", "name": "movie_poster.jpg", "uri": "local:thumbnails/movie_poster.jpg", "dir": "thumbnails", "bytes": 20481 } ]
}
```

`started_at` (per task) lets a client compute a naive linear ETA
(`elapsed/(progress/100) - elapsed`) without a dedicated endpoint — this is
exactly what `weft dashboard` does for the currently-selected job.

`GET /jobs?include_progress=true` additionally computes `overall_progress`
per row in the list response (opt-in: computing it costs one extra query per
returned job, so it's off by default for a plain `weft jobs list`).

### `GET /jobs/{id}/tasks/{taskID}/log`

Returns the captured stderr tail (ffmpeg/whisper) of one task:
`{ "task_id": "...", "log": "..." }`. Empty `log` means nothing was captured
(task hasn't run yet, or produced no output).

### `GET /jobs/{id}/assets/{name}`

Returns one produced asset as a base64 data URI — grab a thumbnail or other
small output directly from the API without touching storage:
`{ "name": "...", "uri": "...", "mime": "image/jpeg", "data": "<base64>" }`.
Capped at **10 MB**; an oversized asset returns `413 asset_too_large` — fetch
large files (full `.ts`/`.mp4` segments) from storage directly instead.

### `DELETE /jobs/{id}`

Removes a job and all its data (tasks, events, outputs, logs) — refused
(`409 job_active`) while the job is still queued/reserved/running/uploading/
paused/resumed; cancel it first. By default also deletes the job's published
files from destination storage (best-effort — a delete failure on one asset
is logged, not fatal, and never blocks clearing the DB record); pass
`?purge_files=false` to keep the files (e.g. a shared/symlinked destination
path).

### `PATCH /jobs/{id}/priority`

```json
{ "priority": "emergency" }
```

Changes a job's priority while it's still `queued` or `reserved` — once a
task has actually started running, reordering the queue can no longer affect
it, so this returns `409 job_not_queued` past that point rather than
silently no-op-ing. `400 invalid_priority` for an unknown value.

### `POST /jobs/{id}/{action}`

Actions: `cancel` → `cancelled` · `retry` → `retry` · `pause` → `paused` ·
`resume` → `resumed`. Returns `409 invalid_transition` for illegal transitions
(enforced by the state machine).

### `POST /workers/scale`

```json
{ "count": 8 }
```

Resizes the running worker pool at runtime, capped by `workers.max` when set
(`> 0`). No daemon restart needed.

### `POST /storage/rebuild-master`

Regenerates `playlist.m3u8` in a destination directory from files actually on
disk. Recovers a lost/corrupted master without re-running the job.

```json
{ "destination_id": 0, "path": "Series-Test/movie1" }
```

Response `200`:

```json
{ "status": "ok",
  "renditions": ["360p", "480p", "720p", "1080p"],
  "subtitles": [ { "lang": "fa", "uri": "subtitle/fa/movie.vtt", "name": "movie" } ],
  "audios": [],
  "master": "#EXTM3U\n..." }
```

### `POST /keys`

```json
{ "name": "mykey", "scopes": ["jobs:read", "jobs:write"] }
```

`201 Created`: `{ "id": "key_...", "key": "wft_live_..." }` — **the raw key is
returned exactly once**, never again.

### `POST /webhooks`

```json
{ "url": "https://example.com/hook", "secret": "s3cret",
  "events": ["job.completed", "job.failed"] }
```

`201 Created`: `{ "id": "wh_..." }`. `events: ["*"]` subscribes to everything.

### `GET /system`

Host snapshot: `num_cpu`, `load1/5/15`, `cpu_percent`, `mem_total/used/avail`,
`mem_percent`, `disk_total/used/avail` (of the storage base path),
`disk_percent`, `hostname`, `uptime_seconds`.

### `GET /metrics`

Prometheus text format (`text/plain; version=0.0.4`).

### `GET /config/export` / `POST /config/import`

`GET /config/export` returns the running config as YAML — the same shape
`weft.yaml` is authored in, so it round-trips straight back into `config
import` or `--config`. Secrets (`security.admin_api_key`, webhook secrets,
`ai_subtitle.gemini.api_key`) are redacted (`<redacted>`) by default; add
`?include_secrets=true` to get the real values (same auth/scope either way —
this is about accidental leakage in logs/screenshots, not access control).

`POST /config/import` validates a full config body (rejecting a
`<redacted>` placeholder rather than silently writing it over a real secret)
and writes it to the file the daemon was started with (`weft serve
--config <path>`). **It does not hot-reload** — most config (listen address,
worker pool, plugins, storage) is only read at startup, so the response
includes `"applied": false` and a note that `weft serve` needs a restart to
pick it up. Returns `501 not_configured` if the daemon wasn't started with an
explicit `--config` path.

### `GET /cron` / `POST /cron/{job}/run`

`GET /cron` lists the three built-in jobs (`cleanup`, `benchmark`,
`health_scan`) — schedule, last run, next run, last error if any.
`POST /cron/{job}/run` triggers one immediately, sharing the exact same
run+bookkeeping path the scheduler's own ticker uses. `404
cron_job_not_found` for an unknown name; a known job that runs but fails
still returns `200` with `{"status":"failed","error":"..."}` (the *trigger*
succeeded, the job itself didn't) — 404 is reserved for routing, not job
outcomes.

`cleanup` prunes terminal-status jobs past `cron.cleanup.retention_hours` and
events past `cron.cleanup.event_retention_days` (in addition to the
always-on hourly interval pruners — the cron entry is what makes this
inspectable/triggerable by hand, not a second independent mechanism).
`benchmark` re-runs the node benchmark. `health_scan` samples host resources
and publishes a `node.health` webhook event — raw data collection; a
composite Health Score on top of these samples is a future refinement, not
built yet.

---

## 5. Webhooks

Event-sourced outbox: every event commits in the same transaction as the state
change, then is delivered **at-least-once** to matching webhooks.

Delivery headers:

```
X-Weft-Event: <wire-name>
X-Weft-Signature: <hex(HMAC-SHA256(secret, body))>
```

Retries: up to `max_retries` (8 by default) with exponential backoff; a
webhook that keeps failing puts events in a dead-letter log, replayable with
`POST /webhooks/{event_id}/replay`.

### Wire events

| Wire name | Emitted when |
|---|---|
| `job.created` | Job submitted |
| `job.started` | First task begins |
| `job.progress` | Overall progress update |
| `task.progress` | A task reports progress (ffmpeg/whisper) |
| `job.paused` / `job.resumed` | Pause / resume |
| `job.cancelled` | Cancelled by a user |
| `job.completed` | All tasks done, upload finished |
| `job.failed` | A task failed (job retries or goes to dead_letter) |
| `storage.uploaded` / `storage.failed` | Upload outcome |
| `pipeline.started` / `pipeline.finished` | Whole pipeline bounds |
| `plugin.started` / `plugin.finished` | A single plugin ran |
| `node.joined` / `node.left` | Daemon membership (single-node: startup/shutdown) |
| `node.health` | Published by the `health_scan` cron job — a host resource snapshot (raw data; no composite Health Score yet) |

### Webhook payload

```json
{
  "id": "evt_...",
  "kind": "job.completed",
  "job_id": "job_...",
  "task_id": "",
  "timestamp": "...",
  "payload": { }
}
```

Verify signatures by recomputing `HMAC-SHA256(secret, body)` over the raw
request body and comparing to the header.

---

## 6. AI subtitles

The `ai-subtitle` profile runs the `ai_subtitle` plugin and then uploads.
Supported inputs: `mp4 mkv mov m4a mp3 wav flac`.

### Providers

| Provider | What it does | Needs |
|---|---|---|
| `whisper` | Local transcription with whisper.cpp (offline). | `whisper-cli` binary + model file |
| `gemini` | Direct transcription via the Gemini API (raw audio, single long cue). | `gemini.api_key` |
| `hybrid` | whisper transcribes → Gemini **translates** (or proofreads). | whisper + `gemini.api_key` |

`provider` is selectable per job (`--provider whisper|gemini|hybrid`) and
defaults to `ai_subtitle.provider` from config.

### Source vs target language

- `--src-lang` (`src_lang`): the language the audio is actually spoken in →
  passed to whisper as `-l`.
- `--lang` (`lang`): the language the subtitles must end up in → also names the
  output folder (`subtitle/<lang>/`).
- `src_lang == lang` → hybrid **proofreads** the transcript (fix grammar,
  keep timestamps).
- `src_lang != lang` → hybrid **translates** the transcript to the target
  language. Timestamps are preserved exactly.

Example: transcribe English audio and deliver Persian subtitles:

```bash
weft jobs create film.mp4 --profile ai-subtitle --provider hybrid --src-lang en --lang fa
```

### whisper.cpp pipeline

1. Weft extracts the audio track to **16 kHz mono WAV** with ffmpeg
   (`-vn -ac 1 -ar 16000`) — whisper.cpp cannot read mp4/mkv containers.
2. Runs: `whisper-cli -m <model> -f <in.wav> -of <base> -osrt -l <srcLang>
   [-t <threads>] [--temperature <t>] [--prompt "<...>"] -pp`
3. Reads `<base>.srt`.
4. In `hybrid` mode, sends the SRT to Gemini for translation/proofreading.

Progress is streamed from whisper's `-pp` output (`progress = 42%`) and scaled
into the task's 5–70% window; the API/Gemini phase finishes 70–100.

### whisper.cpp install (Linux)

```bash
dnf install -y cmake gcc-c++ git   # or apt
git clone https://github.com/ggerganov/whisper.cpp /opt/whisper.cpp
cd /opt/whisper.cpp
cmake -B build --buildtype=Release
cmake --build build --config Release -j $(nproc)
cp build/bin/whisper-cli /usr/local/bin/

mkdir -p /opt/weft/models
curl -L -o /opt/weft/models/ggml-medium.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin
```

### Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `ai_subtitle: whisper provider requires model_path` | Config error — set `ai_subtitle.whisper.model_path` |
| `whisper failed: ... (stderr: ...)` | The real whisper error is in the job's `error`. Check the model path, binary, or permissions |
| `whisper produced no srt` | whisper-cli didn't write output — check stderr (binary too old, bad model) |
| `ai_subtitle: hybrid requires gemini.api_key` | `--provider hybrid` needs `gemini.api_key` |
| `gemini returned 4xx: ...` | Bad API key, quota, or the model id doesn't exist |

---

## 7. Profiles

A profile is a DAG template: task kinds + dependency edges. `weft profiles`
lists them live.

| Profile | Input | Output |
|---|---|---|
| `vod-h264` | video | HLS 360/480/720/1080p + thumbnail + subtitle + master + upload |
| `vod-hevc` | video | same, HEVC codec |
| `vod-encode` | video | same as `vod-h264` minus subtitle/ai_subtitle: HLS 360/480/720/1080p + thumbnail + master + upload |
| `audio` | audio | m4a + upload |
| `audio-hls` | audio | m4a + audio HLS (m3u8 + ts) + upload |
| `thumbnail` | video | poster + sprite + vtt + upload |
| `subtitle-add` | SRT/VTT/ASS | add/replace a subtitle track on an already-published video + master update + upload |
| `dub-add` | audio file | add/replace a dubbed audio track + master update + upload |
| `ai-subtitle` | media | AI subtitles (whisper/gemini/hybrid) + upload |
| `trim-update` | video (original source) | re-trim an already-published video in place: hls + upload, `--trim-start`/`--trim-end` select the new window, `--name` matches the existing published base name |
| `poster-replace` | image (jpg/png/webp) | replace an already-published video's poster image, no ffmpeg involved — the image IS the input, copied straight to the destination |

`subtitle-add` / `dub-add` take `--lang` (track language), `--name` (base
name), and (subtitle-add only) `--forced`/`--default` to set the
`#EXT-X-MEDIA` flags. Re-running with the same `--lang` **replaces** the
track; a new `--lang` adds a second track. The master playlist is rewritten
in place to reference the new track.

`trim-update` and `poster-replace` are **post-hoc** profiles: their "input"
is the original source file (or, for poster-replace, a new image), not the
already-published HLS output — set `--source-server` if that source lives on
a registered remote storage server rather than local disk. Both reuse the
existing trim/upload plugin logic; there's no separate "edit the published
output" engine.

### Output layout

```
data/<job-id>/
├── playlist.m3u8              ← master playlist
├── 360p.m3u8  +  360p_000.ts …
├── 480p.m3u8  +  480p_000.ts …
├── 720p.m3u8  +  720p_000.ts …
├── 1080p.m3u8 + 1080p_000.ts …
├── thumbnails/  (poster / sprite / preview.vtt)
├── subtitle/<lang>/<name>.vtt
└── audio/<lang>/<name>.m3u8 + segments   ← dub / audio input
```

Serve it as an HLS root: `http://<host>/data/<job-id>/playlist.m3u8`.

---

## 8. Job lifecycle & durability

Job states:

```
queued → reserved → running → uploading → completed
                       ↕ paused/resumed
             └──── (any step fails) ──→ failed / retry / dead_letter
```

A paused job resumes into `resumed`, which behaves exactly like `running` for
every subsequent transition (uploading, completed, failed, cancelled,
retry, paused again) — it's not a dead end.

Priority bands (scheduled strictly in this order, and re-evaluated fresh on
every scheduling poll — `PATCH /jobs/{id}/priority` takes effect immediately,
no restart needed): `emergency → high → normal → low → background`. Within
the same band, tasks are served oldest-job-first.

Task states: `pending → ready → leased → running → done` (or `failed`).

**Worker pool sizing**: `workers.max: 0` ("auto") starts one worker per
logical CPU core (bounded below by `workers.min`); `workers.max: N` caps it
explicitly. `POST /workers/scale` (`weft workers scale <n>`) resizes the
pool at runtime.

**Resource-aware scheduling** uses two complementary mechanisms:

- **Gate** (`scheduler.max_cpu_percent` / `max_load_average`, measured):
  every worker consults weft's **own** CPU usage (the process plus the
  ffmpeg/whisper children it spawns — never the whole shared host) before
  picking up a new task. If weft is already over the ceiling, the worker
  idles and the task stays queued. `max_load_average` is an optional host
  load-average ceiling (opt-in; off by default). Thresholds of 0 disable the
  respective check.
- **Budget** (`scheduler.max_estimated_cpu_cores` / `max_estimated_ram_mb`,
  declared): sums each in-flight task's plugin-declared cost
  (`EstimatedCPU`/`EstimatedRAMMB`) and refuses a new claim that would push
  the total over the ceiling. This prevents over-committing *before*
  saturation is even measurable — e.g. not starting 8 HLS encodes at once on
  an 8-core box just because Gate's CPU sampling hasn't caught up yet. A
  single task costlier than the whole budget is still admitted when nothing
  else is reserved, so an undersized budget can't starve every task forever
  — it only ever blocks a *second* concurrent one. Both are 0 (unlimited) by
  default.

Pause/Resume are **real**, not cosmetic: pausing a running job stops the actual
`ffmpeg` process (SIGSTOP on Linux; on platforms without POSIX signals the job
state still flips but the child keeps running), and resuming continues it
(SIGCONT). Pausing while a task is queued simply holds it until resume.

Durability model:

- Every state transition, its event, **and** any matching webhook outbox
  row commit together in **one** SQLite (WAL) transaction — a crash between
  "state changed" and "webhook enqueued" cannot happen, because they're not
  two steps.
- Workers **lease** tasks (`workers.lease_ttl_seconds`). A crashed worker's
  lease expires and the task is **re-queued** — work resumes, never duplicates
  or loses a publish.
- The state machine rejects illegal transitions (`409 invalid_transition`).
- A task exceeding `workflow.default_timeout_seconds` times out.
- Terminal-status jobs and old events are pruned automatically
  (`cron.cleanup.retention_hours` / `event_retention_days`, both also
  triggerable by hand via `weft cron run cleanup`) so job/event history
  doesn't grow without bound.

---

## 9. Security

- **API keys**: created via `POST /keys` or `weft keys create`; hashed with
  argon2id (only the hash is stored). Raw key shown once.
- **Scopes** limit each key to a subset of endpoints.
- **Admin key**: `security.admin_api_key` is the bootstrap token the CLI uses.
  Change it in production — anyone who knows the port can otherwise submit jobs.
- **TLS**: config field reserved (`network.tls`); behind a reverse proxy in
  production.
- **Webhook signatures**: `HMAC-SHA256(secret, body)` in `X-Weft-Signature`.
- Storage server credentials (SSH keys/passwords, S3 keys) are stored in the
  DB and **never** returned by `GET /storage/servers` — only `id`/`type`/
  `host`/`user`/`base_path` (not secret; needed to know where a
  `destination_id` actually resolves on disk).

---

## 10. Operations

| Task | Command |
|---|---|
| Health check | `weft doctor` (exit 0 = healthy) |
| Version | `weft version` |
| Live dashboard | `weft dashboard` |
| Queue summary | `weft queue` |
| Worker status | `weft workers` (`weft workers scale <n>` to resize) |
| Host snapshot | `weft system` |
| CPU/ffmpeg benchmark | `weft benchmark` (`weft benchmark get` for the last recorded result) |
| Prometheus metrics | `weft metrics` |
| Cron jobs | `weft cron list` / `weft cron run <cleanup\|benchmark\|health_scan>` |
| Config backup/restore | `weft config export -o backup.yaml` / `weft config import backup.yaml` (restart to apply) |
| Manual backup | copy `weft.db` (SQLite WAL — safe to copy after checkpoint) |
| Rebuild a broken master | `weft storage rebuild --path Series-Test/movie1` |
| Systemd | run `weft serve --config /opt/weft/weft.yaml` as a service |

### `weft dashboard`

A live terminal view — jobs (running/queued/paused/resumed/uploading),
priority queue, workers, and host resources — refreshed on `--interval`
(default 2s). Arrow keys / `j`/`k` select a job row; `c` cancels, `p` pauses,
`r` resumes, `x` deletes (asks `y`/N to confirm), `enter` refreshes
immediately, `q` quits. Selecting a row also shows that job's per-task
breakdown with a naive ETA (`elapsed/(progress/100) - elapsed`, from the
task's `started_at`). Every action goes through the same REST calls as the
one-shot `weft jobs action`/`weft jobs delete` commands — the dashboard is a
polling+keyboard wrapper around them, not a separate code path.

### Linux deployment

```bash
pkill -f "weft.*serve"
cp /tmp/weft-linux-amd64 /usr/local/bin/weft && chmod +x /usr/local/bin/weft
cd /opt/weft
nohup /usr/local/bin/weft serve --config /opt/weft/weft.yaml > weft.log 2>&1 &
```

Weft finds its config in order: `./weft.yaml` → `/opt/weft/weft.yaml` →
`/etc/weft/weft.yaml` → `~/.weft/weft.yaml`.

---

## 11. Development

```bash
go build ./...          # build
go vet ./...            # static checks
go test ./... -count=1  # unit + integration + chaos
```

- No CGO: SQLite via `modernc.org/sqlite`, S3 SigV4 hand-rolled — one static
  binary. The CLI dashboard is the one exception with real dependencies
  (`charmbracelet/bubbletea`, `bubbles`, `lipgloss`) — still pure Go, no CGO.
- Layers: `core/` (pure stdlib, no I/O) · `runtime/` (store, executor, webhook,
  metrics, api, worker, registry, cron, sysinfo) · `plugins/` (media +
  storage + poster upload) · `profiles/` (DAG templates) · `daemon/`
  (assembly) · `cli/` (thin client + the dashboard TUI) · `e2e/`, `chaos/`,
  `integration/` (tests).
- Test suites: `e2e` runs the real daemon against a fake executor; `chaos`
  injects plugin panics, lease expiries, and webhook failures; `integration`
  runs real ffmpeg when present (skips otherwise).

---

## 12. Integrate programmatically

CLI commands map 1:1 to HTTP endpoints — anything the CLI does, the API can do
(and vice versa). Use the CLI reference table below when writing your own
client.

| CLI | HTTP |
|---|---|
| `weft jobs list` | `GET /jobs` |
| `weft jobs get <id>` | `GET /jobs/{id}` |
| `weft jobs events <id>` | `GET /jobs/{id}/events` |
| `weft jobs log <id> <task_id>` | `GET /jobs/{id}/tasks/{taskID}/log` |
| `weft jobs asset <id> <name>` | `GET /jobs/{id}/assets/{name}` |
| `weft jobs create ...` | `POST /jobs` |
| `weft jobs action <id> <a>` | `POST /jobs/{id}/{a}` |
| `weft jobs priority <id> <p>` | `PATCH /jobs/{id}/priority` |
| `weft jobs delete <id>` | `DELETE /jobs/{id}` |
| `weft queue` | `GET /queue` |
| `weft workers` | `GET /workers` |
| `weft workers scale <n>` | `POST /workers/scale` |
| `weft storage list` | `GET /storage/servers` |
| `weft storage add ...` | `POST /storage/servers` |
| `weft storage rebuild ...` | `POST /storage/rebuild-master` |
| `weft webhooks list/create/delete` | `GET/POST /webhooks`, `DELETE /webhooks/{id}` |
| `weft webhooks replay <event_id>` | `POST /webhooks/{event_id}/replay` |
| `weft keys create/list/delete` | `POST/GET /keys`, `DELETE /keys/{id}` |
| `weft profiles` | `GET /profiles` |
| `weft plugins` | `GET /plugins` |
| `weft metrics` | `GET /metrics` |
| `weft benchmark` / `benchmark get` | `POST /benchmark` / `GET /benchmark` |
| `weft config export/import` | `GET /config/export` / `POST /config/import` |
| `weft cron list` / `cron run <job>` | `GET /cron` / `POST /cron/{job}/run` |
| `weft dashboard` | polls `GET /jobs`, `/queue`, `/workers`, `/system`, `/jobs/{id}`; actions via `POST /jobs/{id}/{a}`, `DELETE /jobs/{id}` |
