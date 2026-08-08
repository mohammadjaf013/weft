# Job Lifecycle

## Target Job States

```text
PENDING
  ↓
QUEUED
  ↓
ASSIGNED
  ↓
PREPARING
  ↓
RUNNING
  ↓
FINALIZING
  ↓
COMPLETED
```

## Current-State Mapping

The current implementation uses these job states:

```text
queued → reserved → running → uploading → completed
```

Mapping to target states:

| Target | Current equivalent | Notes |
|---|---|---|
| `PENDING` | none | Add only if pre-queue validation becomes asynchronous. |
| `QUEUED` | `queued` | Existing state. |
| `ASSIGNED` | `reserved` | Current lease reservation. |
| `PREPARING` | task/plugin-specific | Should become explicit for staging/ffprobe. |
| `RUNNING` | `running` | Existing state. |
| `FINALIZING` | `uploading` | Current final upload/publish phase. |
| `COMPLETED` | `completed` | Existing terminal state. |

## Error Paths

```text
RUNNING
  ↓
FAILED
  ↓
RETRY_WAIT
  ↓
QUEUED
```

```text
RUNNING
  ↓
PAUSED
  ↓
QUEUED
```

```text
RUNNING
  ↓
CANCEL_REQUESTED
  ↓
CANCELLED
```

## Transition Authority

| Transition type | Authorized actor |
|---|---|
| create job | API / CLI through API |
| queue job | Job Manager / Scheduler |
| assign job/task | Scheduler |
| prepare/run/finalize | Worker through authenticated report API or internal worker interface |
| progress update | Worker |
| complete task | Worker, validated by master/state layer |
| fail task | Worker or recovery controller |
| cancel request | API / CLI through API |
| cancel complete | Worker or recovery controller |
| retry wait | Retry controller |
| requeue retry | Scheduler / Retry controller |
| mark orphaned | Recovery controller |
| mark worker offline | Resource Manager / Worker Registry |

## Invariants

1. Terminal states are immutable unless an explicit retry creates a new attempt.
2. State transitions must be persisted with an event atomically.
3. Progress must never move a terminal job backward.
4. Retry must increment attempt metadata.
5. Cancel must be idempotent.
6. Requeue after lease expiry must not duplicate completed artifact publication.
7. Structured failure information must be preserved.
