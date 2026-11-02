# AGENTS.md

This file provides guidance to AI coding agents (Claude Code and others) when working with code in this repository.

librespeed-exporter is a Prometheus exporter that runs [librespeed-cli](https://github.com/librespeed/speedtest-cli) on a schedule and exposes the latest result on `/metrics`. Design decisions and their reasons are in `docs/spec.md`; the phased implementation plan, with what is done, is in `docs/plans/librespeed-exporter-v1-plan.md`. Read the spec before changing behavior, and update it (and `docs/usage.md`) when behavior changes.

## Commands

```sh
make build                 # static binary ./librespeed-exporter, version injected via -ldflags
make test                  # go test -race ./...
make lint                  # golangci-lint v2 (config includes the integration build tag)
make fmt                   # golangci-lint fmt (gofmt + goimports with local prefix)
make integration           # LibreSpeed container + real librespeed-cli + promtool check metrics (needs Docker)
make check-deploy          # promtool check rules/config on deploy/ (needs Docker)
make docker                # container image

go test -race -run TestRunTimeout ./internal/runner/          # single test
go test -race -count=10 ./internal/scheduler/                 # repeat flaky-prone timing tests
```

`make integration` downloads librespeed-cli into `.cache/` with `scripts/fetch-librespeed-cli.sh` (checksum-verified). To run the integration test by hand, start `test/integration/compose.yml` and set `LIBRESPEED_CLI` (required), `LIBRESPEED_SERVER_URL` and `METRICS_OUT`, then `go test -tags integration ./test/integration/...`.

## Architecture

Data flows in one direction, and each package knows only the one below it:

```
cmd/librespeed-exporter ──► scheduler ──► runner (exec librespeed-cli)
        │                       │      └──► clientinfo (getIP.php, when the CLI reports no IP)
        └──► collector ◄────────┘ (RecordSuccess / RecordFailure / SetRunning)
        │
        └──► stability (paused while a speed test runs)

stability and clientinfo build their HTTP clients with httpclient.
```

- `internal/config`: YAML loading with `KnownFields(true)`; defaults are prefilled into the struct before decoding, then server paths are completed. `Validate` returns all errors joined, plus non-fatal warnings separately.
- `internal/runner`: one `librespeed-cli` process per server and per test. The server list goes on stdin (`--local-json -`, single server with `id` 1, `--server 1`) so the CLI can never pick another server. Stdout is capped at 1 MiB, stderr keeps a 4 KiB tail, the environment is filtered to an allowlist, and cancellation sends SIGTERM then SIGKILL after `WaitDelay` (Windows just kills). The CLI printing `null` with exit code 0 means the server failed its availability check (`ErrNoReport`). Unit conversion works on hundredths to avoid float noise (`msToSeconds`, `mbpsToBytesPerSecond`). No Prometheus imports here.
- `internal/scheduler`: ticker-driven cycles that never overlap (a busy flag drops ticks), servers tested sequentially, random 0–10% jitter except on the `run_on_start` cycle. Config and runner are swapped together through an `atomic.Pointer` by `Update`; a cycle reads it once at its start. A test canceled by shutdown is not recorded as a failure. With `collect_client_info`, a successful result without a client IP gets one from `clientinfo.Fetch` (replaceable `clientInfo` field, like `jitter`) before it is recorded.
- `internal/clientinfo`: requests the server's `ip_path` with `isp=true`. librespeed-cli reads IP and ISP only from `rawIspInfo`, which is `""` on servers that do not look up the ISP, so it drops the IP too; `parse` falls back to the IP at the start of `processedString` and rejects non-public addresses (`ErrNotPublic`).
- `internal/httpclient`: the `http.Client` for talking to a LibreSpeed server directly (TLS settings, `ca_file`, `ip_version`, `source_address`) and `URL` to join a server path.
- `internal/collector`: holds per-server state and implements `prometheus.Collector` with const metrics built at scrape time, so servers removed on reload vanish without `DeleteLabelValues`. Result metrics appear only after the first success; counters start at zero for every label combination; a phase must be both enabled now and measured by the stored result to be exposed.
- `internal/stability`: optional continuous latency probing (`stability.enabled`), the equivalent of the LibreSpeed web stability test, which librespeed-cli lacks. One loop per server; latency is request-written to first-byte via `httptrace`; jitter is the absolute difference of consecutive successes (reset on failure or pause). The `Prober` owns its own histogram/counter vecs (not the collector) and drops removed servers on `Update`. `main` wraps the collector in `pausingRecorder` so `SetRunning` from the scheduler also pauses probing.
- `cmd/librespeed-exporter`: flags (kingpin + exporter-toolkit + promslog), startup checks (config, `librespeed-cli --version`), HTTP routes, and `reloader`, shared by SIGHUP and `POST /-/reload`. A reload is all-or-nothing; a changed `librespeed_cli.path` creates and version-checks a new runner before anything is applied, then collector, scheduler and prober are updated.

## Conventions

- Everything in English: code, comments, metric HELP texts, log messages and docs.
- Metrics use Prometheus base units (bytes per second, seconds), not Mbps or ms. The `server` label is the configured `name`, never the URL.
- Never pass `--share` or `--telemetry-*` to librespeed-cli.
- Default listen address is `:9469`, set in `cmd/librespeed-exporter/main.go` and repeated in the Dockerfile `EXPOSE`, `deploy/compose/` and the docs.
- Time-dependent scheduler tests use `testing/synctest`; configure fakes before `Run` starts (see `start(..., opts...)` in `scheduler_test.go`) to avoid races.
- The runner tests re-execute the test binary as a fake librespeed-cli (`TestMain` checks `FAKE_LIBRESPEED_CLI_MODE`); add new CLI behaviors as modes in `fakeCLI`. Fixtures in `internal/runner/testdata/` are real librespeed-cli v1.0.14 output.
- `deploy/grafana/dashboard.json` is generated: edit `deploy/grafana/dashboard.py` and run `make dashboard` (CI fails if they differ). Keep one y-axis per panel, speed axes starting at zero, and status colors only on panels where the value means good or bad.
- Gauge queries in the dashboard aggregate with `max by (server) (...)`; counters use `sum by (server, ...)`. Without it, a replaced pod (new `pod`/`instance` labels, e.g. on Kubernetes) shows up as a duplicate line per server.
- Collector tests compare against golden `.prom` files in `internal/collector/testdata/`; update them when metrics change.
- `librespeed-exporter.example.yml` must load and match `config.Default()`; `TestExampleFileIsValidAndMatchesDefaults` enforces it, so change both together.
- The pinned librespeed-cli version lives in both `Makefile` (`LIBRESPEED_CLI_VERSION`) and `Dockerfile` (`ARG LIBRESPEED_CLI_VERSION`); keep them in sync. Tool versions are pinned in CI (`golangci-lint`, GoReleaser, `prom/prometheus`, LibreSpeed image).
- gosec is disabled for `_test.go` files in `.golangci.yml`; production code uses targeted `#nosec` comments with a reason.

## Releasing

The git tag is the only source of the version: `make build` uses `git describe`, GoReleaser and the image workflow read the tag, and `--version` prints it. To release `vX.Y.Z` (SemVer; before 1.0, new features bump the minor version):

1. Move the `[Unreleased]` entries in `CHANGELOG.md` to a `[X.Y.Z] - date` section and update the compare links.
2. Update the image tag in `README.md` and `deploy/compose/docker-compose.yml` when the minor version changes.
3. Merge to `main` with CI green, then `git tag -a vX.Y.Z -m "librespeed-exporter vX.Y.Z"` on that commit and push the tag. `.github/workflows/release.yml` publishes the binaries and the `ghcr.io` image.
