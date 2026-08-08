# CLI Contract

## Principles

The CLI is a thin API client. It must not implement business logic that bypasses the API.

## Current Commands To Preserve

```text
weft serve
weft init-config
weft doctor
weft version
weft jobs list|get|create|events|action
weft keys create|list|delete
weft webhooks create|list|delete
weft storage list|add
weft queue
weft workers
weft profiles
weft plugins
weft metrics
weft benchmark
weft system
```

## Target Distributed Commands

```text
weft master start
weft worker start
weft worker status
weft worker drain
weft worker jobs
weft job create
weft job status <id>
weft job cancel <id>
weft job retry <id>
weft job pause <id>
weft job resume <id>
weft system status
```

Existing plural command forms should remain compatible unless a documented major-version migration is planned.
