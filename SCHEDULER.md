# Scheduler

## Responsibilities

The scheduler selects runnable tasks and assigns them to suitable workers. It must respect DAG dependencies, priority, worker health, resource constraints, and retry timing.

## Inputs

- queued jobs
- task dependency graph
- job priority
- retry eligibility and backoff deadline
- worker registry
- worker resource snapshots
- worker health state
- worker labels/capabilities
- storage locality when available

## Eligibility

A worker is eligible only when:

1. It is registered and authenticated.
2. It is not offline.
3. It is not draining unless the task is already assigned to it and allowed to finish.
4. It has required capabilities/plugins.
5. It has enough estimated CPU, memory, and disk capacity.
6. It has not exceeded configured concurrency limits.

## Scoring Model

Do not use a single threshold such as `cpu < 80` as the complete algorithm. Use weighted scoring:

```text
workerScore =
    cpuScore
  + memoryScore
  + diskScore
  + activeJobsScore
  + healthScore
  + capabilityScore
  + localityScore
  + priorityCompatibilityScore
```

Suggested defaults:

| Component | Weight | Direction |
|---|---:|---|
| CPU headroom | 25 | more headroom is better |
| memory headroom | 20 | more headroom is better |
| disk headroom | 15 | more headroom is better |
| active jobs | 15 | fewer active jobs is better |
| health | 15 | healthy beats degraded/busy |
| capability | required | missing capability disqualifies |
| locality | 5 | closer input/output is better |
| priority compatibility | 5 | urgent jobs favor high-capacity workers |

## Priority

Priority order:

```text
emergency → high → normal → low → background
```

Priority selects which runnable work is considered first. Worker scoring selects where the work should run.

## Starvation Prevention

Low-priority jobs must not starve forever. Use one or more of:

- priority aging
- reserved background slots
- maximum wait time escalation

## Assignment Contract

An assignment must include:

- job ID
- task ID
- worker ID
- lease ID or fencing token
- attempt number
- lease expiration
- task parameters
- artifact staging policy

The worker must include the lease/fencing token in progress and completion reports.
