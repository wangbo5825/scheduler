# Pogo Scheduler (wangbo5825 fork)

A Caddy / [FrankenPHP](https://frankenphp.dev) scheduler module that embeds a
lightweight timer inside the web server and fires a job on schedule.

This is a feature fork of [y-l-g/scheduler](https://github.com/y-l-g/scheduler).
It keeps the original embedded `command` mode 100% compatible and adds two
major capabilities:

1. **Native HTTP(S) job mode** — schedule an HTTP/HTTPS request instead of
   spawning a system command.
2. **Flexible timing parameters** — custom `interval` and cron `schedule`
   expressions, instead of a hard-coded every-minute tick.

> 本 Fork 的核心目标
> 1. 让 Caddy/FrankenPHP 除了“定时调用外部命令”，还能“定时发起 HTTP/HTTPS 请求”，
>    不需要在 Web 服务器里执行系统命令，降低安全暴露面。
> 2. HTTP(S) 请求直接进入 Caddy/FrankenPHP 的应用处理链路（例如常驻的 PHP Worker），
>    没有每次“起进程”的开销，性能和可控性都更好。
> 3. 调度参数可配置：支持 `interval`（间隔）与 cron `schedule`（定时表达式），
>    兼容原有 `command` 配置，无需修改现有 Caddyfile。

## Why this fork exists

The original module runs `php artisan schedule:run` (or any command) every
minute. That is a good fit for Laravel-style deployments, but it has two
limitations that this fork addresses:

- **Security**: executing commands means the Caddy process must be able to run
  arbitrary binaries on the host. Every scheduled command widens the attack
  surface of the web server.
- **Performance**: each command tick starts a new OS process (PHP CLI bootstrap,
  framework boot, ...). HTTP requests, by contrast, are handled inside the
  already-running FrankenPHP worker pool, where a warm PHP application can
  process them in milliseconds.

The HTTP(S) mode intentionally moves the question of *what the job does* back
into the application. The scheduler only needs to say *when* and *to where*;
your route, worker, or another HTTP service decides *how*. This is also why
FrankenPHP is a great fit: the same binary serves web traffic and scheduled
traffic through the same optimized request pipeline, with no shell and no
external interpreter involved in the trigger itself.

## Project goals

See [docs/GOALS.md](docs/GOALS.md) for the full goal statement and roadmap.

Short version:

- Keep the proven embedded-scheduler model of the upstream project.
- Add HTTP(S) as a first-class job type with method, headers, body, and
  expected-status controls.
- Make scheduling flexible (`interval` and cron expressions), while staying
  backward-compatible with existing `command`-only configurations.
- Keep the module dependency-light and easy to build with `xcaddy`.
- Document security and operational guidance so users can choose between
  `command` and `http` deliberately.

## Installation

### Docker

Build a FrankenPHP binary that includes this module with `xcaddy`. See the
official [FrankenPHP Docker documentation](https://frankenphp.dev/docs/docker/)
for base-image details.

Example Dockerfile from this repository root:

```dockerfile
FROM dunglas/frankenphp:builder AS builder

COPY --from=caddy:builder /usr/bin/xcaddy /usr/bin/xcaddy
COPY . /src/scheduler

RUN CGO_ENABLED=1 \
    XCADDY_SETCAP=1 \
    XCADDY_GO_BUILD_FLAGS="-ldflags='-w -s' -tags=nobadger,nomysql,nopgx" \
    CGO_CFLAGS="$(php-config --includes)" \
    CGO_LDFLAGS="$(php-config --ldflags) $(php-config --libs)" \
    xcaddy build \
        --output /usr/local/bin/frankenphp \
        --with github.com/dunglas/frankenphp=./ \
        --with github.com/dunglas/frankenphp/caddy=./caddy \
        --with github.com/dunglas/caddy-cbrotli \
        --with github.com/wangbo5825/scheduler/module=./src/scheduler/module

FROM dunglas/frankenphp AS runner

COPY --from=builder /usr/local/bin/frankenphp /usr/local/bin/frankenphp
```

For plain Caddy builds, replace the `--with github.com/dunglas/...` lines with:

```bash
xcaddy build --with github.com/wangbo5825/scheduler/module
```

## Configuration

Add a `pogo_scheduler` global option to your Caddyfile. Exactly one job type is
used per scheduler instance: either a command or an HTTP(S) request.

### Mode 1: command (upstream-compatible)

```caddy
{
    pogo_scheduler {
        mode           command
        command        php artisan schedule:run
        dir            /var/www/html
        interval       1m
        timeout        5m
        overlap        allow
        shutdown_grace 30s
    }
}
```

`mode` may be omitted when `command` is used. With no `command` at all, the
default remains `php artisan schedule:run`.

### Mode 2: HTTP(S) request (new)

Minimal example:

```caddy
{
    pogo_scheduler {
        mode     http
        url      http://127.0.0.1:80/artisan/schedule
        interval 1m
        timeout  30s
    }
}
```

Full example with POST JSON, headers, and expected status:

```caddy
{
    pogo_scheduler {
        mode            http
        url             http://127.0.0.1:80/scheduler/run
        method          POST
        header          Authorization "Bearer {$SCHEDULER_TOKEN}"
        header          Content-Type application/json
        body            "{\"source\":\"caddy\"}"
        expect_status   200
        interval        5m
        timeout         30s
        overlap         skip
        shutdown_grace  10s
    }
}
```

When `mode` is omitted but `url` is set, the mode is inferred as `http`.

`mode` is mutually exclusive with the other job type: setting both `command`
and `url` (or `command` together with `mode http`) is a configuration error.

## Configuration reference

| Caddyfile directive | JSON field | Description |
| --- | --- | --- |
| `mode command\|http` | `mode` | Job type. Inferred: `http` if `url` is set, otherwise `command`. |
| `command ...` | `command` | Command to run (command mode only). Default: `php artisan schedule:run`. |
| `dir ...` | `dir` | Working directory for the command (command mode only). |
| `url ...` | `url` | Target URL, `http://` or `https://` (http mode only). |
| `method ...` | `method` | HTTP method (http mode only). Default: `GET`. |
| `header Name value` | `headers` | Request header; repeat for more headers/values (http mode only). |
| `body ...` | `body` | Request body string (http mode only). Quote it in Caddyfile when it contains spaces. |
| `expect_status N` | `expect_status` | Require an exact response status. Default 0 = any 2xx is success. |
| `insecure_skip_verify bool` | `insecure_skip_verify` | Skip TLS verification for the request (use only for internal/self-signed testing). |
| `interval D` | `interval` | Tick period, e.g. `30s`, `5m`, `1h`. Minimum `1s`. Default `1m`. |
| `schedule "cron"` | `schedule` | Cron expression; overrides `interval` when set. |
| `timeout D` | `timeout` | Per-run timeout. Default `5m`. |
| `overlap allow\|skip` | `overlap` | `skip` drops a tick while the previous run is still active. Default `allow`. |
| `shutdown_grace D` | `shutdown_grace` | How long an active run may finish during shutdown. Default `30s`. |

### Scheduling

Two scheduling styles are supported:

**`interval`** — the default `1m` behaves exactly like the original module: one
tick per minute aligned to the wall clock (`:00`). Other intervals are aligned
to the same wall-clock grid where possible, e.g. `5m` fires at `:00`, `:05`,
`:10`, ..., `30s` fires twice per minute. Minimum interval: `1s`.

**`schedule`** — a cron expression that takes precedence over `interval`.
Supported syntax follows `robfig/cron`:

- Standard five-field expressions: `schedule "*/5 * * * *"` (every 5 minutes),
  `schedule "0 3 * * *"` (daily at 03:00).
- Descriptors: `"@hourly"`, `"@daily"`, `"@every 5m"`, etc.
- Timezone prefix: `schedule "CRON_TZ=Asia/Shanghai 0 3 * * *"`.

Example: hit an internal maintenance endpoint every night at 03:00 Shanghai
time:

```caddy
{
    pogo_scheduler {
        mode     http
        url      http://127.0.0.1:80/internal/scheduled-maintenance
        method   POST
        header   Authorization "Bearer {$SCHEDULER_TOKEN}"
        schedule "CRON_TZ=Asia/Shanghai 0 3 * * *"
        timeout  2m
    }
}
```

### HTTP(S) result semantics

- Default: a `2xx` final response is success; any other status is logged as a
  failed run. Redirects are followed by Go's default HTTP client.
- `expect_status 204` (or any exact code): the request succeeds only when the
  final status code equals the configured value.
- Transport errors, DNS failures, and TLS errors are failures.
- Response bodies are logged only on failure, truncated to 4 KiB, to avoid
  accidentally logging secrets or huge payloads.

### Command result semantics (unchanged)

Exit code 0 is success; non-zero exits and timeouts are logged as failures.
Output is logged on failure and on successful output-producing runs.

## How it works

1. **Scheduling goroutine**: the module computes the next tick from the
   configured `interval` (wall-clock-aligned) or cron `schedule`, then sleeps
   until that instant.
2. **Dispatch**: at each tick the module starts one run in a goroutine:
   - `command` mode executes the command as a subprocess bounded by `timeout`;
   - `http` mode performs an HTTP(S) request bounded by the same `timeout`.
3. **Overlap control**: with `overlap skip`, a tick is skipped while the
   previous run is active (shared by both modes).
4. **Graceful shutdown**: on Caddy reload/shutdown, no new runs are started.
   Active runs may finish within `shutdown_grace`; after that they are
   cancelled (process killed / HTTP request context cancelled).

## Security & performance notes

- **Prefer HTTP(S) mode whenever possible.** It avoids placing shell commands
  and interpreter paths in your Caddy config, keeps the web-server process from
  spawning children, and removes per-tick process startup cost.
- **Protect the trigger endpoint.** A scheduled URL is a remote code/action
  trigger. Do not expose it publicly without authentication; use an internal
  host/port, Caddy auth, a firewall rule, or a shared secret header.
- **`insecure_skip_verify`** disables TLS certificate verification and must
  only be used against endpoints you fully control (e.g. self-signed internal
  services during migration). Prefer a proper internal CA or plain HTTP on a
  loopback address.
- **Command mode still exists for cases that genuinely need host processes**
  (e.g. database dumps, image processing, git operations). When you use it,
  treat the Caddyfile as trusted infrastructure config.
- This is a single-node scheduler, not a distributed one. In horizontally
  scaled Kubernetes deployments, prefer native `CronJob` resources or add
  application-level shared locking.

## When to use which mode

| Situation | Mode |
| --- | --- |
| Laravel `schedule:run` semantics with Octane/FrankenPHP | `command` (default, unchanged) |
| The work can run inside your app's HTTP stack | `http` (recommended) |
| Maintenance triggered via a protected route, queue worker ping, health checks | `http` |
| Legacy shell tools, DB dumps, host utilities | `command` |
| Many different jobs with different schedules | Multiple scheduler instances per process are not yet supported; see roadmap. |

## Development

The Go module lives in [`module/`](module/).

```bash
cd module
go test ./...
```

The end-to-end integration test in `module/tests` requires a locally built
`caddy` or `frankenphp` binary in the module directory (CI builds one with
`xcaddy`); unit tests do not.

## Roadmap

See [docs/GOALS.md](docs/GOALS.md).

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## License & upstream

Fork of [y-l-g/scheduler](https://github.com/y-l-g/scheduler). Check upstream
and this repository for license terms before redistribution.
