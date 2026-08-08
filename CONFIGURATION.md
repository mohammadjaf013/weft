# Configuration

## Principles

- All environment-specific behavior must be configurable.
- Defaults should be safe for local development.
- Production examples must not include real credentials.
- Invalid required configuration should fail fast at startup.

## Configuration Areas

- network listen address
- TLS settings
- API key/auth settings
- database path and durability settings
- worker count and lease TTL
- scheduler thresholds and weights
- resource-manager heartbeat intervals
- storage destinations
- FFmpeg/FFprobe paths and timeouts
- AI subtitle provider settings
- webhook retry settings
- metrics settings
- cleanup/staging paths

## Path Rules

No hardcoded production paths. Use config values or environment variables. Temporary work directories must be isolated per job/task/attempt.
