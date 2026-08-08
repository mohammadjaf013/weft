# Development Guide

## Workflow

1. Read `AGENTS.md` and related specs.
2. Inspect existing code and tests.
3. Make the smallest coherent change.
4. Add or update tests.
5. Update documentation.
6. Run formatting and checks.
7. Commit with a clear message.

## Code Organization

- `core/` should remain pure logic and interfaces.
- `runtime/` contains implementations of core interfaces.
- `daemon/` wires components together.
- `cli/` is a thin API client.
- `profiles/` defines job-to-task DAG templates.
- `plugins/` contains media/storage task implementations.
- `docs/` contains user-facing guides.

## Compatibility

Do not remove existing CLI/API behavior without a migration plan. Add new distributed capabilities incrementally.
