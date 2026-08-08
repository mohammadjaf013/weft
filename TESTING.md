# Testing

## Required Checks

Before submitting changes, run the relevant subset and prefer the full suite:

```text
go test ./... -count=1
go vet ./...
go build ./...
```

## Test Categories

- unit tests for pure core logic
- state-machine transition tests
- scheduler priority and scoring tests
- lease expiry and recovery tests
- worker process lifecycle tests
- FFmpeg progress parser tests
- API handler tests
- CLI command mapping tests
- storage plugin tests
- webhook retry/dead-letter tests
- integration tests for full job lifecycle
- chaos tests for crash/retry behavior

## Failure Cases To Cover

- invalid transition
- duplicate completion
- expired lease completion
- worker crash
- master restart
- DB unavailable in target distributed mode
- FFmpeg non-zero exit
- disk full
- upload failure
- webhook receiver unavailable
- cancel during running task
- retry after failed task
