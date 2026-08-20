# uptime-monitor

A concurrent uptime/health-check monitor written in Go. It checks a list of HTTP targets in parallel and reports whether each is up or down, along with response latency.

## Requirements

- Go 1.26+

## Build

```
go build -o monitor.exe ./cmd/monitor
```

## Usage

```
monitor.exe -urls "https://example.com,https://httpbin.org/status/500"
```

| Flag | Default | Description |
|---|---|---|
| `-urls` | `https://example.com` | Comma-separated list of target URLs to check |
| `-timeout` | `5s` | Per-check request timeout |
| `-interval` | `0` | If set, repeat checks on this interval instead of running once (e.g. `30s`) |
| `-fail-threshold` | `3` | Consecutive failures required before a target is marked DOWN and alerted |
| `-db` | `monitor.db` | Path to the SQLite database file check history is written to |
| `-webhook` | *(none)* | If set, POST a Discord-formatted embed here on every DOWN/RECOVERED transition |
| `-listen` | *(none)* | If set, serve a live status dashboard and JSON API on this address (e.g. `:8080`) |
| `-config` | *(none)* | Path to a JSON config file (see `config.example.json`). Flags passed explicitly on the CLI override the matching config value |
| `-header` | *(none)* | Extra header sent with every request, as `"Name: Value"`. Repeatable (`-header "A: 1" -header "B: 2"`). The same headers go to every target in the run |

Each target is checked concurrently on its own goroutine; results are printed as they complete (so order reflects response time, not input order). In single-shot mode (default), the process exits with code `1` if any target is down, `0` if all are up. Every check, in either mode, is recorded to the SQLite database regardless of outcome.

A target is only reported as an `ALERT ... is DOWN` after failing `-fail-threshold` times in a row — a single failed check still prints as `DOWN` for that round, but doesn't trigger an alert-worthy state transition on its own. **This consecutive-failure count is persisted to the SQLite database and reloaded on startup**, so it survives across separate process runs, not just within one long-running `-interval` loop — this matters if you run the monitor as a scheduled single-shot job (cron, GitHub Actions, etc.) instead of a long-running process, since each invocation would otherwise start back at zero and alert on the very first failure.

On a failed check, if the response had a body (e.g. a target with its own JSON `/health` output), a truncated copy (up to 2KB) is captured and shown alongside the status code — both in the per-check output line and in the `ALERT` message, so you can tell "unreachable entirely" (a network-level error, shown as `error=...`) apart from "reached it, and it's reporting something specific is wrong" (a non-2xx/3xx response, shown as `status=... body=...`).

Example (single-shot):

```
$ monitor.exe -urls "https://example.com,https://httpbin.org/status/500,https://thisdomaindoesnotexist12345.com"
DOWN  https://thisdomaindoesnotexist12345.com  error=Get "https://thisdomaindoesnotexist12345.com": dial tcp: lookup thisdomaindoesnotexist12345.com: no such host  latency=72.9246ms
UP  https://example.com  status=200  latency=462.6371ms
DOWN  https://httpbin.org/status/500  status=500  latency=517.0657ms
```

Example (interval mode, showing the consecutive-failure alert and webhook):

```
$ monitor.exe -urls "https://thisdomaindoesnotexist12345.com" -interval 1s -fail-threshold 2 -webhook "https://example.com/hooks/uptime"
monitoring 1 target(s) every 1s, alerting after 2 consecutive failures (Ctrl+C to stop)
DOWN  https://thisdomaindoesnotexist12345.com  error=... latency=53.6ms
DOWN  https://thisdomaindoesnotexist12345.com  error=... latency=0s
ALERT  https://thisdomaindoesnotexist12345.com is DOWN (2 consecutive failures, last: unreachable (dial tcp: ...))
```

The last line above also POSTs a Discord-formatted embed to the webhook URL (`-webhook` expects a Discord incoming webhook URL specifically, not a generic one — Discord requires a `content` or `embeds` field or it rejects the request):

```json
{"embeds":[{"title":"https://thisdomaindoesnotexist12345.com is DOWN","description":"2 consecutive failures, last: unreachable (dial tcp: ...)","color":15548997,"timestamp":"2026-08-19T16:47:35Z"}]}
```

Red for DOWN, green for RECOVERED (Discord's own brand colors). If the webhook request fails or the endpoint returns a non-2xx/3xx status, the error is logged to stderr — it never blocks the check loop or crashes the process.

To get a webhook URL: in Discord, go to the target channel's Settings → Integrations → Webhooks → New Webhook, then copy its URL.

### Checking a protected endpoint

Some targets, like a `/health` endpoint that reports internal status, may require an auth header rather than being publicly open. `-header` (or `headers` in a config file) sends the same header(s) with every check in the run:

```
monitor.exe -urls "https://example.com/health" -header "X-Health-Key: your-secret-here"
```

If that target's response body reports which specific thing is wrong (e.g. `{"status":"down","checks":{"database":{"status":"down"}}}`), a truncated copy of it is captured on any non-2xx/3xx response and included in both the console output and the `ALERT`/webhook message, so you're not stuck with a generic "it's down."

### Status dashboard

```
monitor.exe -urls "https://example.com,https://httpbin.org/status/500" -interval 30s -listen ":8080"
```

Serves:

- `http://localhost:8080/` — an HTML table of every target's latest status, color-coded UP/DOWN, auto-refreshing every 5s
- `http://localhost:8080/api/status` — the same data as JSON

The server runs on its own goroutine alongside the check loop, so it always reflects the latest completed round. If `-listen` is set without `-interval`, the process runs one round of checks and then stays alive just to keep serving that round's results (stop it with Ctrl+C).

### Config file

Instead of retyping flags every run, point at a JSON file:

```
monitor.exe -config config.example.json
```

```json
{
	"urls": ["https://example.com", "https://httpbin.org/status/500"],
	"timeout": "5s",
	"interval": "30s",
	"fail_threshold": 3,
	"db": "monitor.db",
	"webhook": "",
	"listen": ":8080",
	"headers": {}
}
```

Any flag you also pass explicitly on the command line overrides the matching value from the file — e.g. `monitor.exe -config config.example.json -interval 10s` uses everything else from the file but checks every 10s instead of 30s.

### Stopping it

The process shuts down gracefully on Ctrl+C or `SIGTERM`: it stops the check loop, shuts the status server down cleanly (if running), and closes the database before exiting — rather than being killed mid-check. This makes it safe to run under a process supervisor (systemd, Task Scheduler, NSSM, etc.), which is the intended way to run it continuously in the background; the binary itself doesn't register as an OS service.

## Roadmap

- [x] Single-target blocking HTTP check
- [x] Concurrent multi-target checks (goroutines + results channel)
- [x] SQLite persistence + consecutive-failure tracking
- [x] Webhook alerting on state transitions
- [x] Status HTTP endpoint / dashboard
- [x] Config file, graceful shutdown, daemon-friendly operation
