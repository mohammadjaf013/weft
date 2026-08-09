# AGENTS.md — Guide for AI Coding Agents (Claude, etc.)

Read this before making any change to this repo. It exists so different agents — or the same agent across sessions — don't drift from the architecture or duplicate documentation. It describes the **real, current state of the code**, not an aspirational target — if you find this file and the code disagree, trust the code and fix this file in the same change.

## The one rule that matters most

**Core (`core/`) never imports anything outside the Go stdlib and its own interfaces.** No `database/sql`, no `net/http`, no `os/exec`, no FFmpeg/SQLite/S3/cron-library package, ever. If a change to `core/` would require one of those imports, the change belongs in `runtime/` or `plugins/` instead, behind an interface `core/interfaces.go` already defines (or a new one added there). This single rule is why Storage, FFmpeg, and SQLite can all be swapped later — don't compromise it for convenience.

If you're unsure whether something is Core, Runtime, or Plugin: Core is pure decision-making (state transitions, DAG resolution, event routing, priority ranking). Runtime is "how do we actually do it" (SQLite writes, ffmpeg calls, HTTP, cron scheduling, host resource sampling). Plugins are "one specific capability" (encode video, upload to S3, generate a thumbnail, replace a poster image).

Two small but real exceptions to "Core has no dependencies": `runtime/webhook` and `runtime/store/sqlite` both need the same event→wire-name mapping, so it lives in `core/wirename.go` (`core.WireName`) rather than being duplicated — it's pure data with zero I/O, so it doesn't violate the spirit of the rule, just the letter of "only interfaces."

## Where documentation lives — single source of truth per topic

Don't duplicate content between files. Each topic has exactly one home; everywhere else links to it.

| Topic | Lives in | Everything else does |
|---|---|---|
| Every REST endpoint, CLI command, config key, request/response shape | `docs/REFERENCE.md` | `README.md` and the Persian docs (`docs/SETUP-FA.md`, `docs/GUIDE-FA.md`, `docs/CLI-API-FA.md`) point here for the full listing — they don't restate it |
| Quick start, project pitch, links | `README.md` | short only — no deep content, just pointers into `docs/` |
| DB schema | `runtime/store/sqlite/store.go`'s `migrate`/`migrateColumns` (the schema IS the code — there is no separate schema doc to keep in sync) | `docs/REFERENCE.md` §2 documents config, not the DB schema directly |
| Persian-language install/end-user guides | `docs/SETUP-FA.md` (admin install), `docs/GUIDE-FA.md` (end-user), `docs/CLI-API-FA.md` (CLI↔API mapping, Persian) | these carry real narrative content the English REFERENCE.md doesn't duplicate — keep them, but don't let their command/endpoint *listings* drift from REFERENCE.md |

**This repo has exactly one reference manual (`docs/REFERENCE.md`), not a split `api.md`/`cli.md`/`config.md`.** Do not create a second architecture doc, a second API reference, or restate the REST/CLI/config surface in a new file "for convenience" — extend `docs/REFERENCE.md`. If it's grown unwieldy, split it and update every inbound link in the same change — never leave two files describing the same thing (this is exactly the kind of drift that put `docs/REFERENCE.md` out of sync with `docs/CLI-API-FA.md` in the past — the Persian doc had `jobs log`/`jobs asset`/`jobs delete`/`workers scale` weeks before the English one did).

**When you change code, update `docs/REFERENCE.md` in the same commit — not "later":**

- New/changed REST endpoint → §4 REST API (endpoint table + a worked example if the shape isn't obvious)
- New/changed CLI command or flag → §3 CLI reference
- New/changed config key → §2 Configuration
- New Plugin → add to `plugins.enabled` in §2's example, and to the profile/plugin tables in §7 if it's user-facing
- New profile → §7 Profiles table
- New Event kind / wire event → §5 Webhooks wire-events table

## Non-negotiable properties — don't regress these

Any change must preserve:

1. **No Crash** — plugin calls stay wrapped in the Plugin Sandbox (`runtime/registry/registry.go`'s `Process`, `recover()`-wrapped); don't add a plugin call path that bypasses the registry.
2. **No Missed Data** — outbox rows are enqueued in the **same SQLite transaction** as the triggering state change (`runtime/store/sqlite/store.go`'s `insertEventTx`/`enqueueOutboxTx`) — this was a real bug once (an async bus-subscriber enqueuer that could drop a delivery on a crash) and was fixed by moving enqueue into the transaction. Don't reintroduce an async enqueue path; any new state-changing operation that should notify externally must go through `SaveJobEvent`/`SaveTaskEvent`/`AppendEvent`, not a direct/inline webhook call.
3. **No Missed/Wrong Conversion** — a Job only reaches `completed` after its output is checksummed (`verified: true`). Don't add a path that marks a Job complete without verification.
4. **CLI ⇄ REST parity** — never add a CLI-only feature. Every `cli/*.go` command is a thin HTTP client (`cli/client.go`'s `client.get/post/patch/del`) over a real endpoint; add the endpoint first (or in the same change) and have the CLI call it. `docs/REFERENCE.md` §12 is the parity table — keep it in sync.
5. **Job/task state transitions only happen through the state machine** — `core/statemachine.go`'s `allowedTransitions` map is the only place a legal (from, to) pair is defined. If you add a new `JobStatus`, add its row to `allowedTransitions` too (a status with no outgoing transitions is a dead end — this was a real bug: `JobResumed` had no entries and a resumed job could never reach `completed`).
6. **Settings validated before run, not discovered mid-job** — any new plugin or provider with required config (an API key, a model path, credentials) must be checked in `configs.Config.Validate()`, the same way `ai_subtitle` is, so `weft serve`/`weft doctor` refuse to start in a broken state rather than accepting a Job that's guaranteed to fail later.

## Boundaries to respect (confirmed against the real code, not aspiration)

- **Destination/source servers use PER-SERVER credentials (SSH key path or password, S3 access/secret key), not a shared keypair.** `plugins/storage/ssh/ssh.go`'s `Config` takes `KeyPath`/`Password` per registered server. This is a deliberate, confirmed decision — don't build a shared-weft-managed-keypair feature; it was considered and rejected as unnecessary complexity for the actual deployment model.
- Publishing/going-live is the Admin app's responsibility, triggered by the `job.completed` webhook. Don't add a `publish` state or step inside Weft — that duplicates a decision that already lives elsewhere.
- `plugins/rebuild/rebuild.go` (directory-scan master reconstruction) hardcodes subtitle `DEFAULT=NO`/`FORCED=NO` — this is a **known, intentional limitation**, not a bug to silently fix: rebuild has no durable per-track metadata store to recover a previously-requested flag from (it only sees files on disk). `plugins/masterupdate/masterupdate.go` (the `subtitle-add`/`dub-add` path, which has real request params at the moment a track is added) is where forced/default are actually honored. Don't "fix" rebuild's hardcoding without first building that metadata store — it's a real, separate feature, not an oversight.
- Remote job **inputs** (not just outputs) are supported via `job.SourceServerID` (a relative path resolved against a registered storage server) or a direct `http(s)://` URL — both fetched into a per-job scratch cache under `<WorkRoot>/cache/<jobID>/`, cleaned up by `daemon/cleanup.go`'s `cleaner` on job completion. A bare `s3://`/`ssh://` `input_ref` with no `source_server_id` is rejected loudly (`daemon/serve.go`'s `resolveInput`) — there's no credential to fetch it with. Don't add ad-hoc URL-scheme credential guessing; route new remote-source needs through `SourceServerID` and the existing `core.Storage` abstraction.
- Cluster-only concepts (multi-node discovery, node labels/taints, cross-node tracing) don't exist in this codebase and aren't being built — this is a single-node daemon by design. Don't add scaffolding for them speculatively.

## Before you finish a change

1. Does it still compile with `core/` importing nothing beyond stdlib + its own interfaces (check `core/*.go`'s import blocks)?
2. Did you add/update tests at the right layer? Core: pure unit tests against `core.NewMemStore()`/`core.NewEventBus()`, no SQLite. Runtime/store: integration tests against `sqlite.OpenInMemory()`. Plugins: contract tests using `ffexec.NewFake(...)` so no real ffmpeg is needed (see any `plugins/*/*_test.go`). `e2e/` runs the real daemon end-to-end against a fake executor; `chaos/` injects plugin panics, lease expiries, and webhook failures; `integration/` runs real ffmpeg when present (skips otherwise). Run `go build ./...`, `go vet ./...`, and `go test ./...` before considering a change done — `go test -race` is not available in every environment (requires CGO/a C toolchain); don't assume its absence from CI output means races were checked.
3. Did you update `docs/REFERENCE.md` — not create a new doc file?
4. If you added an endpoint or CLI command, does §12's parity table reflect it, and does the new scope (if any) exist in both `runtime/api/server.go`'s `scopeAllowed` AND the admin key's scope list in `runtime/api/keys.go`? (A scope missing from the second list means the admin key itself gets `403` on the new endpoint — a real bug shape in this codebase.)
5. If you touched the `jobs` table schema, did you add the column via `migrateColumns` (`ALTER TABLE ... ADD COLUMN`, idempotent — see the existing entries) rather than only the `CREATE TABLE IF NOT EXISTS` block, or existing databases won't pick it up? Did you thread the new field through **all four** places `core.Job` is read/written in `runtime/store/sqlite/store.go` (`SaveJobEvent`, `LoadJob`, `ListJobs`, `ListActiveJobs`) — missing one is silent, not a compile error.
6. If you changed a shared helper's contract (e.g. `core.AssetRef`), remember `Storage.Save`/`Open` resolve purely from `Name`/`URI` — the `Dir` field is metadata the *caller* folds into `Name` before calling Save (see `plugins/upload/upload.go`'s `rel := ...Dir... + Name` pattern); the storage backends themselves never read `Dir`. This has bitten a new plugin before (`plugins/posterupload`) — don't assume `Dir` is honored automatically.
