# Database

## Responsibilities

The database stores durable state, event history, outbox records, worker metadata, leases, and recovery information.

## Required Entities

- jobs
- tasks
- events
- task attempts
- leases
- workers
- worker heartbeats
- storage destinations
- webhook subscriptions
- webhook outbox and dead letters
- API keys
- benchmarks / capability snapshots where applicable

## Transaction Rules

1. A state change and its event must commit atomically.
2. A task assignment and lease/fencing token must commit atomically.
3. Webhook delivery must use outbox semantics.
4. Retry attempt increments must be atomic with retry scheduling.
5. Completion must be idempotent and protected by attempt/fencing metadata.

## Migration Rules

1. Schema changes require migrations.
2. Migrations must be forward-only unless rollback is explicitly supported.
3. Migrations must be tested with existing data where practical.
4. New columns must have safe defaults or nullable transition plans.

## Database Outage Target

Current local SQLite mode is durable for single-daemon operation. Distributed mode must additionally define behavior when the master database is temporarily unavailable. Workers should continue safe local execution and sync later using attempt/fencing metadata.
