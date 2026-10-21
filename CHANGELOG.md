# Changelog

## Unreleased

### Added

- **HTTP(S) job mode** (`mode http`): schedule HTTP/HTTPS requests instead of
  external commands. No shelling out from Caddy and no per-tick process
  startup; requests are handled inside the running application pipeline
  (e.g. a warm FrankenPHP worker).
  - `url`, `method`, repeatable `header`, `body`, `expect_status`, and
    `insecure_skip_verify` configuration.
  - Any 2xx response is success by default; `expect_status` requires an exact
    status code.
  - Response bodies are only logged on failure and truncated to 4 KiB.
- **Flexible scheduling parameters**:
  - `interval`: configurable tick period (default `1m`, minimum `1s`),
    wall-clock-aligned like the original every-minute behavior.
  - `schedule`: cron expressions (standard 5-field, descriptors such as
    `@every 5m`, and `CRON_TZ=` timezone prefix) via `robfig/cron`.
- `mode` is inferred when omitted: `http` when `url` is set, otherwise
  `command`, so existing command-only Caddyfiles remain valid.
- **Multiple named jobs** in one scheduler instance:
  - Caddyfile: repeat `job <name> { ... }` blocks.
  - JSON: use the `jobs` array.
  - Each job has an independent mode, target, schedule, timeout, and overlap
    policy.
  - Job names must be unique; the legacy flat single-job configuration remains
    fully supported.
  - `shutdown_grace` remains global for all active jobs.
- Unit tests for the HTTP mode, multi-job parsing, scheduling, defaults, and
  validation.

### Unchanged behavior

- Embedded minute-aligned scheduler for FrankenPHP/Caddy.
- Command mode with configurable command, working directory, timeout, overlap
  mode, and shutdown grace period.
- Single-process trigger only; no distributed locking; not recommended for
  Kubernetes production scheduling without application-level locks.
