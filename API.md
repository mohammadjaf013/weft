# API Contract

## Principles

- JSON over HTTP.
- Stable structured errors.
- Bearer API keys with scopes when authentication is enabled.
- No secrets in responses.
- Idempotency keys for create/cancel/retry operations should be supported in distributed mode.

## Core Endpoints

```text
POST   /jobs
GET    /jobs
GET    /jobs/{id}
GET    /jobs/{id}/events
POST   /jobs/{id}/cancel
POST   /jobs/{id}/retry
POST   /jobs/{id}/pause
POST   /jobs/{id}/resume

GET    /queue
GET    /workers
GET    /workers/{id}
POST   /workers/register
POST   /workers/{id}/heartbeat
POST   /workers/{id}/drain

GET    /profiles
GET    /plugins
GET    /storage/servers
POST   /storage/servers
POST   /storage/rebuild-master
GET    /health
GET    /metrics
```

The current implementation may expose action endpoints as `/jobs/{id}/{action}`. Keep backward compatibility when adding explicit action paths.

## Error Shape

```json
{
  "error": {
    "code": "invalid_transition",
    "message": "cannot transition running to queued",
    "details": {}
  }
}
```

## Job Create Shape

Target shape:

```json
{
  "profile": "vod-h264",
  "priority": "normal",
  "input": {
    "uri": "local:/data/input.mp4",
    "size_bytes": 123456789,
    "checksum": "sha256:optional"
  },
  "output": {
    "destination_id": 1,
    "path": "asset-123"
  },
  "options": {
    "video": { "enabled": true, "resolutions": [360, 720, 1080] },
    "audio": { "enabled": true },
    "subtitles": { "enabled": true, "formats": ["vtt"] },
    "encryption": { "enabled": false, "method": "AES-128" },
    "thumbnails": { "enabled": true }
  }
}
```

## Worker Heartbeat Shape

See `RESOURCE_MANAGER.md` for the canonical heartbeat payload.
