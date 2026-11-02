# librespeed-exporter: specification

Status: implemented, first release pending
Date: 2026-11-02

## Goal

librespeed-exporter periodically measures download speed, upload speed, latency and jitter against one or more [LibreSpeed](https://github.com/librespeed/speedtest) servers and publishes the results in the Prometheus format.

Measurements are made by [librespeed-cli](https://github.com/librespeed/speedtest-cli), the project's official command line client. The exporter handles scheduling, turning results into metrics and the HTTP server; it does not implement the test protocol.

The main use case is tracking the quality of an internet link over time (home, office, branch) and alerting when it drops below what was contracted.

Optionally, the exporter also probes each server's latency continuously, the equivalent of the LibreSpeed web stability test, to catch short drops that a speed test every 30 minutes misses.

### Out of scope for v1

- A web UI to run tests by hand.
- Storing history. That is Prometheus's job.
- Being a LibreSpeed server. The exporter is a client only.
- Sending results to the librespeed.org telemetry (`--share`).

## Main decisions

### Language and libraries

Go, which is the standard for exporters and the language librespeed-cli is written in.

| Use | Library |
|---|---|
| Metrics | `github.com/prometheus/client_golang` |
| HTTP server, TLS and basic auth | `github.com/prometheus/exporter-toolkit` |
| Flags | `github.com/alecthomas/kingpin/v2` (what exporter-toolkit uses) |
| Configuration | `go.yaml.in/yaml/v3` (the maintained continuation of the archived `gopkg.in/yaml.v3`) |
| Logging | `log/slog` through `github.com/prometheus/common/promslog` |

### librespeed-cli as a child process

The exporter runs the `librespeed-cli` binary with `--json` and reads the report from stdout. Tested with v1.0.14.

librespeed-cli is also a Go module (`github.com/librespeed/speedtest-cli`), and its `defs` and `speedtest` packages could be imported. That was ruled out because, in v1.0.14:

- the `Download`, `Upload` and `PingAndJitter` methods of `defs.Server` use the global `http.DefaultClient`, which the `speedtest` package reconfigures (timeout, transport, source address). In a long-running process this would affect every other HTTP use in the exporter;
- none of those methods take a `context.Context`, so a test cannot be canceled halfway;
- progress and error output goes through the global `output` package, which is designed for a terminal;
- the only documented public API is the command line. The internal packages can change between versions without notice.

Running a child process isolates each test in its own process, allows killing a stuck test, and lets librespeed-cli be upgraded without rebuilding the exporter.

The one exception is stability probing (see below). librespeed-cli has no such mode, and the web stability test is nothing more than repeated `GET` requests to the ping path, so the exporter sends those itself.

### Scheduled tests, not per scrape

A speed test takes 30 to 40 seconds and uses real bandwidth. Running one on every Prometheus scrape would make scrapes slow, tie the results to `scrape_interval`, and could use tens of GB per day.

So the exporter runs tests in the background on a configurable interval, and `/metrics` always answers right away with the latest result. A scrape never triggers or waits for a test.

Only one `librespeed-cli` process runs at a time. Two concurrent tests would compete for the same link and both results would be wrong. When several servers are configured, they are tested one after another within each cycle.

## librespeed-cli integration

### Locating the binary

The binary path comes from `librespeed_cli.path` in the configuration. If it is empty, the exporter looks up `librespeed-cli` in `PATH`.

At startup the exporter runs `librespeed-cli --version`, extracts the version from the first line and exposes it in `librespeed_cli_info`. If the binary is missing or the command fails, the exporter does not start.

### One run per server

The exporter starts a separate process for each configured server. A failure or timeout on one server does not affect the others, and each result belongs to exactly one server.

The server list is passed on stdin with `--local-json -`. It holds only the server being tested, with `id` 1, and `--server 1` selects it. This avoids temporary files and keeps librespeed-cli from downloading the public list and picking another server on its own.

The JSON sent on stdin for the `home` server of the example configuration:

```json
[{"id":1,"name":"home","server":"https://speed.example.com/","dlURL":"backend/garbage.php","ulURL":"backend/empty.php","pingURL":"backend/empty.php","getIpURL":"backend/getIP.php"}]
```

### Generated command line

Always present:

```
librespeed-cli --json --local-json - --server 1 --duration <s> --concurrent <n> --chunks <n> --upload-size <KiB> --timeout <s>
```

Added depending on the configuration:

| Configuration | Flag |
|---|---|
| `test.download: false` | `--no-download` |
| `test.upload: false` | `--no-upload` |
| `test.ping_method: http` | `--no-icmp` |
| `test.ip_version: 4` or `6` | `--ipv4` or `--ipv6` |
| `test.source_address` | `--source <ip>` |
| `test.interface` | `--interface <name>` |
| `test.no_preallocate: true` | `--no-pre-allocate` |
| `servers[].insecure_skip_verify: true` | `--skip-cert-verify` |
| `servers[].ca_file` | `--ca-cert <file>` |

The exporter never passes `--share` or any `--telemetry-*` flag, so no result leaves the machine beyond the test traffic itself.

The process only inherits `PATH`, `HOME`, `SYSTEMROOT` (needed by Go programs for networking on Windows) and the proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and their lowercase forms), so its behavior does not depend on where the exporter was started.

### Output format

With `--json`, librespeed-cli writes to stdout an array with one report per tested server:

```json
[{
  "timestamp": "2026-11-02T10:15:03.123456789-03:00",
  "server": {"name": "home", "url": "https://speed.example.com/"},
  "client": {"ip": "203.0.113.10", "hostname": "", "city": "", "region": "", "country": "", "loc": "", "org": "AS64500 Example Telecom", "postal": "", "timezone": ""},
  "bytes_sent": 912261120,
  "bytes_received": 953155584,
  "ping": 12.34,
  "jitter": 1.02,
  "upload": 480.12,
  "download": 495.03,
  "share": "",
  "tls": {"version": "TLS 1.3", "cipher": "TLS_AES_128_GCM_SHA256"}
}]
```

`ping` and `jitter` are in milliseconds and `download` and `upload` in megabits per second (base 1000), all rounded to two decimals. The exporter converts them to base units: seconds and bytes per second. The conversion works on the value in hundredths, an integer, so 0.03 ms becomes exactly `3e-05` s. The `tls` field only appears for HTTPS servers.

The exporter decodes only the fields it needs and ignores the rest, so newer librespeed-cli versions that add fields still parse.

### Failure detection

A run counts as a failure when:

- the process exits with a non-zero code;
- the process is killed for exceeding `timeout`;
- stdout is not valid JSON in the format above;
- the array is empty or `null`. librespeed-cli does this when the server fails its initial availability check: it prints `null`, exits with code zero and, with `--json`, writes nothing to stderr;
- download or upload is enabled and its value is zero.

On every failure the exporter logs the exit code and the last 4 KiB of the process stderr. Stdout is read with a 1 MiB limit; past that, the run is aborted and counts as a failure.

### Timeout and cancellation

Each run uses `exec.CommandContext` with a context bounded by the configured `timeout`. When the timeout expires or the exporter shuts down, the process gets `SIGTERM` and, if it has not exited within 5 seconds, `SIGKILL` (`cmd.Cancel` and `cmd.WaitDelay`). Windows has no `SIGTERM`, so there the process is killed right away.

librespeed-cli's own `--timeout` is something else: the timeout of each HTTP request within the test. It is exposed as `test.http_timeout`.

### ICMP or HTTP ping

By default librespeed-cli measures latency over ICMP and, if ICMP fails (missing permission, firewall), silently falls back to HTTP. The two methods give different numbers, and that silent switch could mix both in the same series.

So the exporter defaults to `ping_method: http`, which passes `--no-icmp` and always measures over HTTP. With `ping_method: icmp`, ICMP is tried first, with the same HTTP fallback as librespeed-cli. On Linux, unprivileged ICMP requires the process group to be within `net.ipv4.ping_group_range` (in a container: `--sysctl net.ipv4.ping_group_range="0 2147483647"`).

librespeed-cli always takes 10 ping samples. That cannot be changed from the command line.

## Stability probing

The LibreSpeed web UI (since v6.2.0) has a stability test, `stability.html`, that sends a `GET` to `backend/empty.php` at most every 200ms for a fixed duration and reports latency, jitter and the share of failed requests. librespeed-cli does not have it.

The exporter offers the same measurement as continuous probing, which fits Prometheus better than a fixed-length test: with `stability.enabled: true`, every configured server gets one request to its `ping_path` every `stability.interval` (default 1s), indefinitely. Each request carries a random `r` parameter and `Cache-Control: no-cache`, like the web client. It is off by default, so upgrading does not start sending requests to public servers nobody asked to probe.

- **Latency** is the time from the request being written to the first response byte (`net/http/httptrace`), which leaves out connection setup the way the web test does with the browser's Performance API. Connections are kept alive between probes.
- **Failures** are transport errors, non-200 responses and probes exceeding `stability.timeout` (default 5s). They are request failures, not packet loss.
- **Jitter** is the absolute difference between two consecutive successful probes. It is recorded in a histogram instead of the web test's weighted moving average, so the mean jitter over any window is `rate(sum) / rate(count)`. A failure or a pause resets it, so no jitter is computed across a gap.
- **Pause during speed tests.** While librespeed-cli runs, the saturated link would show up as latency spikes every `interval`. The scheduler pauses probing for the duration of each speed test.
- **Network settings.** Probes honor each server's `insecure_skip_verify` and `ca_file`, and the test's `ip_version` and `source_address`, plus the proxy environment variables. `test.interface` is not applied, since binding to an interface is OS-specific.
- **Reload.** A reload restarts the probe loops with the new settings. Servers that remain keep their histograms and counters; removed servers lose their series. A server whose `ca_file` cannot be parsed is logged and not probed.
- Failures are logged once when a server starts failing and once when it recovers, not on every probe.

## Metrics

All metrics use the `librespeed_` prefix and Prometheus base units (seconds and bytes). Speed is exposed in bytes per second; in Grafana, use the `bytes/sec(SI)` unit, or multiply by 8 and use `bits/sec(SI)` to see Mbps.

The `server` label is the `name` set in the configuration. The user picks it and it stays the same when the URL changes, which avoids broken series.

### Test result

| Metric | Type | Labels | Description |
|---|---|---|---|
| `librespeed_download_bytes_per_second` | gauge | `server` | Download speed of the last successful test |
| `librespeed_upload_bytes_per_second` | gauge | `server` | Upload speed of the last successful test |
| `librespeed_ping_seconds` | gauge | `server` | Mean latency of the last successful test |
| `librespeed_jitter_seconds` | gauge | `server` | Jitter of the last successful test |

These four metrics appear only after the first successful test against the server. When a test fails, the previous values stay exposed; use `librespeed_last_success_timestamp_seconds` to tell whether they are stale. A phase disabled in the configuration produces no metric. A re-enabled phase only reappears after the next test that measured it, so the exporter never exposes a zero nobody measured.

### Run state

| Metric | Type | Labels | Description |
|---|---|---|---|
| `librespeed_up` | gauge | `server` | 1 if the last test succeeded, 0 if it failed |
| `librespeed_last_test_timestamp_seconds` | gauge | `server` | Unix time the last test finished, successful or not |
| `librespeed_last_success_timestamp_seconds` | gauge | `server` | Unix time the last successful test finished |
| `librespeed_test_duration_seconds` | gauge | `server` | Duration of the last `librespeed-cli` process, start to finish |
| `librespeed_tests_total` | counter | `server`, `result` | Tests run; `result` is `success` or `failure` |
| `librespeed_transferred_bytes_total` | counter | `server`, `direction` | Sum of `bytes_received` (`direction="download"`) and `bytes_sent` (`direction="upload"`) of successful tests |
| `librespeed_test_running` | gauge | | 1 while a `librespeed-cli` process is running |
| `librespeed_config_last_reload_successful` | gauge | | 1 if the last configuration load worked, 0 if it failed |

`librespeed_up`, `librespeed_last_test_timestamp_seconds` and `librespeed_test_duration_seconds` appear from the server's first test on, successful or not. The counters are exposed from the start with value zero, for every label combination, so `rate()` and `increase()` work from the first increment.

`librespeed_transferred_bytes_total` exists to track how much of the link the exporter itself uses, which matters on metered plans. Failed tests are not counted, because librespeed-cli reports no byte counts for them.

### Info

| Metric | Type | Labels | Description |
|---|---|---|---|
| `librespeed_server_info` | gauge (always 1) | `server`, `url` | Maps the server name to the configured URL |
| `librespeed_client_info` | gauge (always 1) | `server`, `ip`, `isp` | Public IP (`client.ip`) and ISP (`client.org`) from the last test. Off by default |
| `librespeed_cli_info` | gauge (always 1) | `version` | Version of the librespeed-cli in use |
| `librespeed_exporter_build_info` | gauge (always 1) | `version`, `revision`, `goversion`, ... | Exporter version |

### Stability

Present only while `stability.enabled` is true, for every server being probed.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `librespeed_stability_latency_seconds` | histogram | `server` | Latency of successful probes |
| `librespeed_stability_jitter_seconds` | histogram | `server` | Absolute latency difference between consecutive successful probes |
| `librespeed_stability_requests_total` | counter | `server`, `result` | Probes sent; `result` is `success` or `failure` |

The histograms use exponential buckets from 0.5ms to about 4s, and are also exposed as native histograms to clients that negotiate the protobuf format.

`librespeed_client_info` is off by default (`collect_client_info: false`) because it exposes the public IP and starts a new series every time the IP changes, which is common on residential connections. When the server reports neither IP nor ISP (as servers on the local network do), the metric is not produced.

The exporter also exposes the standard process and Go runtime metrics from `client_golang`.

### Example output

```
# HELP librespeed_download_bytes_per_second Download speed measured in the last successful test.
# TYPE librespeed_download_bytes_per_second gauge
librespeed_download_bytes_per_second{server="home"} 6.187875e+07
# HELP librespeed_ping_seconds Mean latency measured in the last successful test.
# TYPE librespeed_ping_seconds gauge
librespeed_ping_seconds{server="home"} 0.01234
# HELP librespeed_up Whether the last test against the server succeeded.
# TYPE librespeed_up gauge
librespeed_up{server="home"} 1
# HELP librespeed_tests_total Number of tests run, by result.
# TYPE librespeed_tests_total counter
librespeed_tests_total{result="failure",server="home"} 2
librespeed_tests_total{result="success",server="home"} 140
```

## Configuration

Configuration lives in a YAML file. Command line flags only cover the process itself (listen address, config file path, logging).

### Flags

| Flag | Environment variable | Default | Description |
|---|---|---|---|
| `--config.file` | `LIBRESPEED_EXPORTER_CONFIG` | `librespeed-exporter.yml` | Configuration file path |
| `--web.listen-address` | | `:9469` | HTTP server address |
| `--web.telemetry-path` | | `/metrics` | Metrics path |
| `--web.config.file` | | | exporter-toolkit TLS and basic auth file |
| `--log.level` | | `info` | `debug`, `info`, `warn` or `error` |
| `--log.format` | | `logfmt` | `logfmt` or `json` |
| `--version` | | | Print the version and exit |

### Configuration file

Every field with its default value. The `test` defaults match librespeed-cli's, except `ping_method`.

```yaml
# Time between the start of two test cycles.
interval: 30m

# Run a cycle right after startup instead of waiting for the first interval.
run_on_start: true

# Maximum duration of one librespeed-cli run. When exceeded, the process is killed.
timeout: 2m

# Expose librespeed_client_info with the public IP and ISP.
collect_client_info: false

librespeed_cli:
  path: ""                  # empty = look up librespeed-cli in PATH

test:
  download: true
  upload: true
  duration: 15s             # --duration, applies to download and upload
  concurrent: 3             # --concurrent, parallel connections
  chunks: 100               # --chunks, size of each download (the chunk size depends on the server)
  upload_size: 1024KiB      # --upload-size, size of each POST
  http_timeout: 15s         # --timeout, per HTTP request
  ping_method: http         # http or icmp
  ip_version: 0             # 0 = automatic, 4 or 6
  source_address: ""        # --source
  interface: ""             # --interface
  no_preallocate: false     # --no-pre-allocate

stability:
  enabled: false            # continuous latency probing, see "Stability probing"
  interval: 1s              # at least 200ms
  timeout: 5s

servers:
  - name: home           # required, unique, becomes the "server" label
    url: https://speed.example.com/
    # Optional fields. Paths are relative to url; the defaults are the PHP backend's.
    download_path: backend/garbage.php
    upload_path: backend/empty.php
    ping_path: backend/empty.php
    ip_path: backend/getIP.php
    insecure_skip_verify: false
    ca_file: ""
```

`duration`, `http_timeout` and `timeout` take Go duration strings, but librespeed-cli only accepts whole seconds. `duration` and `http_timeout` values that are not a multiple of 1s are rejected.

### Validation

The exporter refuses to start, with a clear log message, when:

- no server is configured;
- two servers share a `name`, or a `name` is empty;
- a `url` is not HTTP or HTTPS;
- `interval` or `timeout` is not greater than zero;
- `concurrent` or `chunks` is less than 1, or `upload_size` is not a positive multiple of 1 KiB;
- `ping_method` is neither `http` nor `icmp`, or `ip_version` is not 0, 4 or 6;
- `source_address` is not an IP address;
- `source_address` and `interface` are both set (librespeed-cli does not accept both);
- `timeout` is less than `phases × duration + 15s`, where `phases` is the number of enabled phases among download and upload. The 15s cover process startup, the IP lookup, ping and opening the connections;
- `stability.interval` is under 200ms (the floor the web stability test uses), or `stability.timeout` is not greater than zero;
- `ca_file` points to a file that does not exist.

If `interval` is shorter than a full cycle (number of servers times `timeout`), the exporter only logs a warning, because the scheduler already prevents overlap (see below).

### Reload

The exporter rereads the file on `SIGHUP` or `POST /-/reload`. If `librespeed_cli.path` changed, the new binary must pass the same `--version` check as at startup. If the new file is invalid, the previous configuration stays in effect and the error goes to the log and to `librespeed_config_last_reload_successful`. A run in progress finishes with the old configuration. Servers removed from the configuration disappear from the metrics.

## HTTP endpoints

| Path | Description |
|---|---|
| `/` | Simple HTML page linking the other endpoints |
| `/metrics` | Metrics in the Prometheus format |
| `/healthz` | Returns 200 while the process is up |
| `/-/reload` | `POST` reloads the configuration |

TLS and basic auth are set up through the `--web.config.file` file, in the standard exporter-toolkit format.

## Scheduling

- The scheduler uses an `interval` ticker. If a cycle is still running when the next tick arrives, the tick is dropped and logged at `warn` level. Cycles never overlap.
- Within a cycle, servers are tested in configuration order.
- Each scheduled cycle starts after a random delay of 0 to 10% of the interval. This keeps several exporters started together (a fleet of branch offices, say) from testing the same server in the same second. The `run_on_start` cycle has no such delay, so the first result shows up shortly after startup.
- On `SIGTERM` or `SIGINT`, the running `librespeed-cli` process is stopped as described in "Timeout and cancellation" and the exporter exits. A test stopped this way does not count as a failure, since it says nothing about the server.

## Bandwidth use

Each test transfers roughly `speed × duration` in each direction. On a symmetric 500 Mbps link with `duration: 15s`, that is about 940 MB downloaded and 940 MB uploaded per test. With the default 30-minute interval and one server, it adds up to about 90 GB per day.

The user documentation must make this calculation visible. On metered links, the options are a longer `interval` or a shorter `duration`. librespeed-cli has no data volume limit, so neither does the exporter.

## Logging

Structured logs through `slog`. Each run produces one `info` line when it ends, with server, result, speeds, latency, jitter, duration and bytes transferred. Failures include the exit code and the tail of librespeed-cli's stderr. At `debug` level, the exporter logs the full command line of each run.

## Packaging and distribution

- Exporter binaries for linux (amd64, arm64, armv7), darwin (amd64, arm64) and windows (amd64), built with GoReleaser. armv7 and arm64 cover the Raspberry Pi, a common place to run this kind of monitoring. These binaries do not include librespeed-cli, which has to be installed separately.
- A multi-architecture container image with the exporter and librespeed-cli. The Dockerfile downloads a pinned librespeed-cli release from GitHub and verifies its checksum. The version is a build `ARG`, to make upgrades easy. Base `gcr.io/distroless/static-debian13:nonroot`. Platforms `linux/amd64`, `linux/arm64` and `linux/arm/v7`, built with `docker buildx` in the release workflow.
- librespeed-cli is distributed under LGPL-3.0. The image includes its license file in `/usr/share/licenses/librespeed-cli/`. Since the exporter only runs the binary and does not embed it, the exporter's own license can be chosen independently.
- Example systemd unit in `deploy/systemd/`.
- Example `docker-compose.yml` in `deploy/compose/` with the exporter, Prometheus (with the alert rules), Grafana (with the dashboard) and a local LibreSpeed server for testing.
- Example alert rules and Grafana dashboard in `deploy/`.

### Example alerts

```yaml
groups:
  - name: librespeed
    rules:
      - alert: LibrespeedTestFailing
        expr: librespeed_up == 0
        for: 1h
      - alert: LibrespeedResultsStale
        expr: time() - librespeed_last_success_timestamp_seconds > 3 * 3600
      - alert: LibrespeedDownloadLow
        # 100 Mbps = 12.5e6 bytes/s
        expr: avg_over_time(librespeed_download_bytes_per_second[3h]) < 12.5e6
```

## Repository layout

```
cmd/librespeed-exporter/   main: flags, HTTP server, signals, reload
internal/config/           YAML loading, defaults and validation
internal/runner/           builds the command line, runs librespeed-cli and parses its JSON
internal/scheduler/        test cycles and single-run control
internal/collector/        stores the latest results and exposes the metrics
internal/stability/        continuous latency probing with its own HTTP client and metrics
test/integration/          end-to-end test with the real librespeed-cli
deploy/                    systemd, compose, alerts, dashboard
docs/                      this specification, the user guide and plans
```

`internal/runner` knows nothing about Prometheus. It takes a `context.Context`, a server's configuration and the test parameters, and returns a struct with the result already in base units, or an error.

## Tests

- Unit tests in `internal/runner` for building the command line from every configuration combination.
- JSON parser unit tests with fixtures recorded from librespeed-cli v1.0.14: normal output, upload disabled, server down (`null`), plus empty array, truncated JSON, unknown extra fields and zero download.
- Execution tests with a fake librespeed-cli, re-executing the test binary itself (`os.Args[0]`, with an environment variable selecting the behavior). The fake covers success, exit code 1, a process that hangs until the timeout, one that ignores `SIGTERM`, and output over the limit.
- Unit tests in `internal/config` for the defaults and every validation rule.
- Stability tests against `httptest` servers: latency and jitter samples, failures and their log transitions, timeouts, pausing, reload, `ip_version`, `source_address` and TLS settings.
- Collector tests comparing `/metrics` output with expected files (`testutil.CollectAndCompare`).
- An optional integration test (build tag `integration`) with the real librespeed-cli against the official LibreSpeed container image, run in CI. It also catches JSON format changes when librespeed-cli is upgraded.
- `promtool check metrics` in CI to validate metric names and types.

## Resolved questions

1. **Default port.** `9469`. The [Prometheus default port allocations](https://github.com/prometheus/prometheus/wiki/Default-port-allocations) list this port for script_exporter; it was kept on purpose and is not registered for this project.
2. **Bytes or bits.** Bytes, following the Prometheus convention. Dashboards multiply by 8 to show Mbps.

## Open questions

1. **Automatic server selection.** Without `--local-json` and `--server`, librespeed-cli downloads the public librespeed.org list and tests the server with the lowest latency. It would be easy to offer that as an `auto` server, but the chosen server can change between cycles and mix results in one series. Proposal: leave it for v2, with the chosen server exposed in `librespeed_server_info`.
2. **Probe mode.** A blackbox_exporter-style `/probe?target=...` endpoint would let Prometheus decide when to test. It stays out of v1 for the reasons in the scheduling section, but could come later behind a flag.
3. **Precision.** librespeed-cli rounds ping, jitter and speeds to two decimals in its JSON. For ping on fiber links (under 1 ms) that can be coarse. If it becomes a problem, the alternative is to propose an unrounded field upstream.
