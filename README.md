# Pogo Scheduler

A small [Caddy](https://caddyserver.com) / [FrankenPHP](https://frankenphp.dev)
module that acts as an embedded cron trigger for constrained or single-binary
PHP deployments.

It runs lightweight Go timers and fires one or more jobs on schedule. Each job
is either:

1. **A command** (the original behavior), such as
   `php artisan schedule:run`; or
2. **A native HTTP(S) request** (new), which keeps the web server free of
   shell access and avoids per-tick process startup.

Pogo Scheduler is intentionally not a distributed scheduler. In Kubernetes
production environments, prefer native `CronJob` resources.

## Production status

Pogo Scheduler is a small experimental module for constrained, single-binary,
and single-node PHP deployments. Its API may change.

It is not a distributed scheduler. In horizontally scaled deployments, run it
as a singleton service or rely on application-level shared locks. In Kubernetes
production environments, prefer native `CronJob` resources.

## Why HTTP(S) mode?

The historical mode executes a system command every minute. That is a good fit
for Laravel-style deployments, but it has two costs:

- **Security**: the Caddy process must be able to run arbitrary binaries on the
  host, widening the attack surface of the web server.
- **Performance**: every tick starts a new OS process (PHP CLI bootstrap,
  framework boot, ...).

HTTP(S) mode moves the *what the job does* question back into the application:
the scheduler only says *when* and *to where*, and a warm FrankenPHP worker or
any other HTTP service handles the work through the already-running request
pipeline. No shell, no external interpreter, no process startup per tick.

## Installation

### 1. Docker

Build a FrankenPHP binary that includes Pogo Scheduler with `xcaddy`. See the
official [FrankenPHP Docker documentation](https://frankenphp.dev/docs/docker/)
for the base image details.

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
        --with github.com/dunglas/frankenphp/caddy=./caddy  \
        --with github.com/dunglas/caddy-cbrotli \
        --with github.com/y-l-g/scheduler/module=./src/scheduler/module

FROM dunglas/frankenphp AS runner

COPY --from=builder /usr/local/bin/frankenphp /usr/local/bin/frankenphp
```

Then copy your app and `Caddyfile` into the runner image as usual.

### 2. Configure Caddyfile

Add a `pogo_scheduler` block to your Caddyfile. A scheduler can run one legacy
single job, or multiple named jobs with independent schedules and options.

#### Mode 1: command (original behavior)

Example for Laravel:

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

#### Mode 2: HTTP(S) request (new)

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

### Multiple jobs

Use `job <name> { ... }` blocks to run multiple jobs in one Caddy process.
Each job has its own mode, target, schedule, timeout, and overlap policy:

```caddy
{
    pogo_scheduler {
        shutdown_grace 30s

        job backup {
            mode     http
            url      http://127.0.0.1:80/internal/backup
            method   POST
            schedule "0 3 * * *"
            timeout  10m
            overlap  skip
        }

        job cleanup {
            mode     command
            command  php artisan cleanup
            dir      /var/www/html
            interval 5m
            timeout  2m
            overlap  skip
        }

        job heartbeat {
            mode     http
            url      http://127.0.0.1:80/internal/heartbeat
            interval 30s
        }
    }
}
```

Job names must be unique. `shutdown_grace` is global: during Caddy shutdown,
all active jobs share that grace period, after which remaining runs are
cancelled. Top-level job options cannot be mixed with `job` blocks; put all
job-specific options inside each block.

In JSON, the same configuration is represented with a `jobs` array:

```json
{
  "jobs": [
    {
      "name": "backup",
      "mode": "http",
      "url": "http://127.0.0.1:80/internal/backup",
      "method": "POST",
      "schedule": "0 3 * * *",
      "timeout": "10m",
      "overlap": "skip"
    },
    {
      "name": "cleanup",
      "mode": "command",
      "command": ["php", "artisan", "cleanup"],
      "interval": "5m"
    }
  ],
  "shutdown_grace": "30s"
}
```

## Configuration reference

| Caddyfile directive | JSON field | Description |
| --- | --- | --- |
| `job <name> { ... }` | `jobs[]` | A named job with independent mode, target, schedule, timeout, and overlap options. |
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
| `shutdown_grace D` | `shutdown_grace` | Global grace period for active runs during shutdown. Default `30s`. |

### Scheduling

Two scheduling styles are supported:

**`interval`** — the default `1m` behaves like the original module: one tick per
minute aligned to the wall clock (`:00`). Other intervals are aligned to the
same wall-clock grid where possible, e.g. `5m` fires at `:00`, `:05`, `:10`,
..., `30s` fires twice per minute. Minimum interval: `1s`.

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

### Command result semantics

Exit code 0 is success; non-zero exits and timeouts are logged as failures.
Output is logged on failure and on successful output-producing runs.

## How It Works

1. **Scheduling goroutines**: each job computes its next tick from its own
   `interval` (wall-clock-aligned) or cron `schedule`, then sleeps until that
   instant.
2. **Dispatch**: at each tick the module starts one run in a goroutine:
   - `command` mode executes the command as a subprocess bounded by `timeout`;
   - `http` mode performs an HTTP(S) request bounded by the same `timeout`.
3. **Overlap control**: each job tracks its own active run; with
   `overlap skip`, a tick is skipped while that job's previous run is active.
4. **Graceful shutdown**: on Caddy reload/shutdown, no new runs are started.
   Active runs across all jobs may finish within the global `shutdown_grace`;
   after that they are cancelled.

## Security notes

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

## Overlap Behavior

The default starts a run on every tick:

```caddy
overlap allow
```

This matches Laravel's `schedule:run` behavior. Sub-minute tasks such as
`everySecond()` keep `schedule:run` alive for most of the minute, and a small
overrun at `:00` should not skip the whole next minute.

You can opt into a local overlap guard:

```caddy
overlap skip
```

Use `skip` only when the job must never overlap. If one run is still active at
the next tick, the new run is skipped and a warning is logged.

## When To Use It

Good fits:

- Single VPS or single-server deployments.
- Docker Compose or small Docker setups where you want to avoid `cron`,
  `supervisord`, or a separate scheduler container.
- PaaS environments where the app has one long-running web process but no
  system cron.
- Development and staging environments where you want scheduler behavior
  without host setup.
- A dedicated singleton scheduler service using the same application image.

Poor fits:

- Kubernetes production. Use native `CronJob` resources instead.
- Horizontally scaled web replicas unless your application uses shared locks,
  such as Laravel `onOneServer()` / `withoutOverlapping()` with a shared cache
  backend.
- Critical exactly-once workflows without application-level idempotency and
  locking.
