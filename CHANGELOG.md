# Changelog

## 0.1.0 (2026-09-06) — wangbo5825 fork

### Added

- **HTTP(S) job mode** (`mode http`): schedule HTTP/HTTPS requests instead of
  external commands. No shelling out from Caddy, no per-tick process startup,
  and the request is handled inside the running application pipeline
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
- Go module path changed to `github.com/wangbo5825/scheduler/module` so the
  fork can be built with `xcaddy --with github.com/wangbo5825/scheduler/module`.
- Unit tests for the HTTP mode and scheduling logic; README and project goals
  documentation (`docs/GOALS.md`).

### Unchanged behavior

- Embedded minute-aligned scheduler for FrankenPHP/Caddy.
- Command mode with configurable command, working directory, timeout, overlap
  mode, and shutdown grace period.
- Single-process trigger only; no distributed locking; not recommended for
  Kubernetes production scheduling without application-level locks.

## Upstream history

See the git history and the original
[y-l-g/scheduler](https://github.com/y-l-g/scheduler) repository for changes
prior to this fork.
