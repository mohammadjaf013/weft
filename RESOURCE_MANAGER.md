# Resource Manager

## Purpose

The Resource Manager collects worker resource state, derives health, and feeds the scheduler.

## Worker Heartbeat Payload

Workers should periodically report:

```json
{
  "worker_id": "worker_a",
  "status": "healthy",
  "timestamp": "2026-08-08T00:00:00Z",
  "cpu": {
    "usage_percent": 72.4,
    "cores": 40,
    "load1": 18.2,
    "load5": 21.4,
    "load15": 22.1
  },
  "memory": {
    "total_bytes": 128000000000,
    "used_bytes": 72000000000,
    "available_bytes": 56000000000
  },
  "disk": {
    "total_bytes": 4000000000000,
    "used_bytes": 2100000000000,
    "available_bytes": 1900000000000
  },
  "network": {
    "rx_bytes_per_second": 120000000,
    "tx_bytes_per_second": 80000000
  },
  "jobs": {
    "active": 4,
    "max": 8
  },
  "capabilities": ["ffmpeg", "ffprobe", "hls", "thumbnail"]
}
```

## Health States

| State | Meaning | Schedulable |
|---|---|---|
| `HEALTHY` | normal capacity | yes |
| `BUSY` | high utilization but acceptable | yes, lower score |
| `OVERLOADED` | unsafe utilization | no for new work |
| `DRAINING` | finishing current work only | no for new work |
| `OFFLINE` | heartbeat expired | no |
| `DEGRADED` | partial capability or resource issue | maybe, low score |

## Threshold Guidance

Thresholds are configuration, not constants. Suggested defaults:

- heartbeat interval: 10s
- heartbeat timeout: 30s
- CPU busy: 75%
- CPU overloaded: 90%
- memory busy: 80%
- memory overloaded: 92%
- disk busy: 80%
- disk overloaded: 90%

## Required Metrics

- `worker_cpu_usage_percent`
- `worker_memory_usage_percent`
- `worker_disk_usage_percent`
- `worker_active_jobs`
- `worker_heartbeat_age_seconds`
- `worker_health_state`
- `scheduler_worker_score`
