# Fork Goals & Roadmap

This document states why the `wangbo5825/scheduler` fork exists, what it aims
to deliver, and where the project is heading. It is the project's "about"
source of truth, mirrored in the repository description and README.

## Problem statement (为什么要做这个 Fork)

The upstream [y-l-g/scheduler](https://github.com/y-l-g/scheduler) embeds a
minute-based cron trigger into Caddy/FrankenPHP and executes an external
command (typically `php artisan schedule:run`).

Calling a system command from the web server has three practical costs:

1. **Security / attack surface (安全)**
   Caddy must be able to run arbitrary binaries and therefore carries a full
   host-process capability. A typo, a compromised config, or an unexpected
   environment can lead to command execution in the web-server context.
2. **Performance (性能)**
   Every tick starts a new OS process: PHP-CLI bootstrap plus framework boot.
   With FrankenPHP the same PHP application is already running as a worker, so
   the *same work* can be triggered through a request in milliseconds instead of
   process-spawn time.
3. **Portability / architecture (架构)**
   Command mode depends on which interpreters and tools exist inside the
   container. HTTP(S) mode has no such dependency: it only needs an address the
   web server can reach.

## Goals

### v0.1.0 (released 2026-09-06)

- [x] Keep upstream `command` mode and its Caddyfile syntax backward-compatible.
- [x] Add `mode http`: schedule HTTP/HTTPS requests.
  - [x] `url`, `method`, `headers`, `body`.
  - [x] Success/failure semantics: 2xx by default or exact `expect_status`.
  - [x] Per-request `timeout` reuse and graceful cancellation on shutdown.
  - [x] Optional `insecure_skip_verify` for controlled self-signed use.
- [x] Add timing parameters:
  - [x] `interval` with a wall-clock-aligned grid (default unchanged: 1m).
  - [x] `schedule` with cron expressions, including `CRON_TZ=` timezone support.
- [x] Publish the fork module path
  (`github.com/wangbo5825/scheduler/module`) and update CI/docs.
- [x] Unit tests for HTTP mode, scheduling, defaults, and validation.

### Future (next milestones)

- Multiple named jobs per process, each with its own mode, schedule, and
  options (requires turning the app into a job registry).
- HTTP request retries with backoff and jitter.
- Webhook/HMAC signing helpers so trigger endpoints can verify requests.
- Custom CA bundle / client certificate configuration for internal HTTPS.
- Prometheus metrics for ticks, durations, and failures.
- Optional "run once now" admin trigger through Caddy's admin API.
- Reconsider cron/interval semantics for DST transitions with an explicit
  timezone field.

## Design decisions

### Why HTTP(S) instead of a shell-out

- The scheduler remains a *trigger*, not a *runner*. Work stays in the
  application, where it can use the framework's routing, middleware, auth,
  queues, and locks.
- FrankenPHP's PHP workers are long-running; a scheduled request is cheap and
  reuses warm state (OPcache, framework singletons).
- The web-server binary no longer needs shell access, interpreters, or
  scheduler-specific tools in the image.
- Failure handling is natural: HTTP status codes and response logging, bounded
  by the same timeout/overlap/shutdown controls as commands.

### Why both `interval` and cron `schedule`

- `interval` keeps the module's original mental model (periodic, minute-aligned)
  and adds sub-minute and multi-minute cadences.
- cron `schedule` covers "specific times" (e.g. 03:00 daily) and familiar
  expressions, without pulling in a full distributed scheduler.
- The default (`interval 1m`) is behaviorally identical to upstream, so
  existing users are unaffected.

### Compatibility contract

- A Caddyfile without `mode`, `url`, `interval`, or `schedule` behaves exactly
  like upstream `pogo_scheduler`.
- `mode` is only required to be explicit when disambiguating configs; it is
  inferred as `http` when `url` is present.
- No system cron, no distributed locking, no leader election. Single-node
  semantics are documented as a deliberate limit.

## Non-goals

- Replacing Kubernetes `CronJob` or system cron for distributed, exactly-once
  scheduling.
- Making Caddy a general-purpose task runner/queue worker.
- Automatic discovery or cluster coordination.

## Repository description (About)

Suggested short description used on GitHub:

> Caddy/FrankenPHP scheduler fork: schedule external commands or native
> HTTP(S) jobs with intervals and cron expressions. Fork of y-l-g/scheduler.
