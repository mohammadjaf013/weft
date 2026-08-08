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

workers:
  min: 1                       # always at least this many workers
  max: 0                       # 0 = auto (derived from cores/load)
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
weft jobs create <input_ref> --profile <name>
weft jobs events <id>
weft jobs action <id> <cancel|retry|pause|resume>

weft keys create <name> <scope...> | keys list | keys delete <id>
weft webhooks create <url> <event...> [--secret S] | list | delete <id>
weft storage list | add <id> <type> [...] | rebuild --path <dir>

weft queue | workers | profiles | plugins | metrics | benchmark | system
```

Global flags (may appear before or after the subcommand): `--api <url>`,
`--key <token>`, `--config <path>`.

### `jobs create` options

| Flag | Meaning |
|---|---|
| `--profile <name>` | Required. Profile/DAG template. |
| `--priority <p>` | `emergency` \| `high` \| `normal` \| `low` \| `background` (default `normal`) |
| `--destination <n>` | Storage server id; `0` = local (default). |
| `--lang <L>` | Subtitle target language (e.g. `fa`, `en`). |
| `--src-lang <L>` | Language the audio is spoken in (whisper `-l`). Differs from `--lang` → hybrid translates. |
| `--provider <p>` | Per-job ai-subtitle provider: `whisper` \| `gemini` \| `hybrid` (empty = server default). |
| `--name <n>` | Base name for output assets (re-running replaces the same track). |
| `--path <p>` | Subdirectory under the destination root (e.g. `movie`, `series`). |
| `--delete-source` | Delete the source file after a successful upload. |

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
| POST | `/jobs/{id}/{action}` | `jobs:write` |
| GET | `/queue` | `queue:read` |
| GET | `/workers` | `workers:read` |
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

### Scopes

`jobs:read` · `jobs:write` · `queue:read` · `workers:read` · `storage:manage` ·
`webhooks:manage` · `keys:manage` · `profiles:read` · `plugins:read` ·
`metrics:read`

### `POST /jobs` — create a job

```json
{
  "input_ref": "/srv/weft/film.mp4",
  "profile": "vod-h264",
  "destination_id": 0,
  "priority": "normal",
  "lang": "fa",
  "src_lang": "en",
  "name": "movie",
  "path": "Series-Test/movie1",
  "provider": "hybrid",
  "delete_source": false
}
```

Response `201 Created`:

```json
{ "id": "job_...", "status": "queued", "tasks": ["task_...", "..."] }
```

Validation errors (`400`): `invalid_request` (malformed / missing fields),
`unknown_profile`, `invalid_priority`, `unknown_destination`, `invalid_provider`.

### `GET /jobs/{id}` — job detail

```json
{
  "id": "job_...",
  "status": "running",
  "priority": "normal",
  "profile": "vod-h264",
  "input_ref": "/srv/weft/film.mp4",
  "destination_id": 0,
  "verified": false,
  "overall_progress": 42.5,
  "error": "",
  "tasks": [
    { "id": "task_...", "kind": "hls", "status": "running",
      "progress_percent": 61.2, "depends_on": [], "error": "" }
  ]
}
```

### `POST /jobs/{id}/{action}`

Actions: `cancel` → `cancelled` · `retry` → `retry` · `pause` → `paused` ·
`resume` → `resumed`. Returns `409 invalid_transition` for illegal transitions
(enforced by the state machine).

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
| `audio` | audio | m4a + upload |
| `audio-hls` | audio | m4a + audio HLS (m3u8 + ts) + upload |
| `thumbnail` | video | poster + sprite + vtt + upload |
| `subtitle-add` | SRT/VTT/ASS | add/replace a subtitle track on an already-published video + master update + upload |
| `dub-add` | audio file | add/replace a dubbed audio track + master update + upload |
| `ai-subtitle` | media | AI subtitles (whisper/gemini/hybrid) + upload |

`subtitle-add` / `dub-add` take `--lang` (track language) and `--name` (base
name). Re-running with the same `--lang` **replaces** the track; a new `--lang`
adds a second track. The master playlist is rewritten in place to reference the
new track.

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
             └──── (any step fails) ──→ failed / retry / dead_letter
```

Priority bands (scheduled strictly in this order): `emergency → high → normal →
low → background`.

Task states: `pending → ready → leased → running → done` (or `failed`).

Durability model:

- Every state transition **and** its event commit in **one** SQLite
  (WAL) transaction — no split-brain between state and log.
- Workers **lease** tasks (`workers.lease_ttl_seconds`). A crashed worker's
  lease expires and the task is **re-queued** — work resumes, never duplicates
  or loses a publish.
- The state machine rejects illegal transitions (`409 invalid_transition`).
- A task exceeding `workflow.default_timeout_seconds` times out.

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
  DB and **never** returned by `GET /storage/servers`.

---

## 10. Operations

| Task | Command |
|---|---|
| Health check | `weft doctor` (exit 0 = healthy) |
| Version | `weft version` |
| Queue summary | `weft queue` |
| Worker status | `weft workers` |
| Host snapshot | `weft system` |
| CPU/ffmpeg benchmark | `weft benchmark` |
| Prometheus metrics | `weft metrics` |
| Manual backup | copy `weft.db` (SQLite WAL — safe to copy after checkpoint) |
| Rebuild a broken master | `weft storage rebuild --path Series-Test/movie1` |
| Systemd | run `weft serve --config /opt/weft/weft.yaml` as a service |

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
  binary.
- Layers: `core/` (pure stdlib, no I/O) · `runtime/` (store, executor, webhook,
  metrics, api, worker, registry) · `plugins/` (media + storage) · `profiles/`
  (DAG templates) · `daemon/` (assembly) · `cli/` (thin client) · `e2e/`,
  `chaos/`, `integration/` (tests).
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
| `weft jobs create ...` | `POST /jobs` |
| `weft jobs action <id> <a>` | `POST /jobs/{id}/{a}` |
| `weft queue` | `GET /queue` |
| `weft workers` | `GET /workers` |
| `weft storage list` | `GET /storage/servers` |
| `weft storage add ...` | `POST /storage/servers` |
| `weft storage rebuild ...` | `POST /storage/rebuild-master` |
| `weft webhooks list/create/delete` | `GET/POST /webhooks`, `DELETE /webhooks/{id}` |
| `weft keys create/list/delete` | `POST/GET /keys`, `DELETE /keys/{id}` |
| `weft profiles` | `GET /profiles` |
| `weft plugins` | `GET /plugins` |
| `weft metrics` | `GET /metrics` |
| `weft benchmark` | `POST /benchmark` |
