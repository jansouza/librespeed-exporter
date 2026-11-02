# Implementation plan: librespeed-exporter v1

Specification: [docs/spec.md](../spec.md)
Repository: [jansouza/librespeed-exporter](https://github.com/jansouza/librespeed-exporter)
Issue: not created yet

## Overview

This plan splits the v1 implementation described in the specification into nine phases. Each phase ends with tested code and can become its own PR.

The order follows the dependencies between packages: configuration first, since every other package uses it; then runner and collector, which do not depend on each other; then the scheduler, which ties them together; and finally `main`, packaging and documentation.

```
1 skeleton ─ 2 config ─┬─ 3 runner ────┬─ 5 scheduler ─ 6 main/HTTP ─ 7 integration ─ 8 packaging ─ 9 docs
                       └─ 4 collector ─┘
```

Decisions that apply to every phase:

- Module path `github.com/jansouza/librespeed-exporter`, Apache-2.0 license.
- Go 1.26, the same version as librespeed-cli v1.0.14.
- Code, comments, metric `HELP` texts, log messages and documentation in English.
- Logging with `github.com/prometheus/common/promslog`, the `log/slog` layer used by exporter-toolkit, which already provides the `--log.level` and `--log.format` flags.
- Time-dependent tests use `testing/synctest` instead of `time.Sleep`.

## Phases

### Phase 1: project skeleton

Status: [x] done

**Intent.** A repository that builds and runs lint and tests in CI before any logic exists.

**Expected outcome.** `make build` produces `librespeed-exporter`, which answers `--version`. `make test` and `make lint` pass. CI runs both on every push and PR.

**Tasks**

- [x] Git repository with the `jansouza/librespeed-exporter` GitHub remote.
- [x] `go mod init github.com/jansouza/librespeed-exporter`.
- [x] `LICENSE` (Apache-2.0) and `.gitignore`.
- [x] Minimal `cmd/librespeed-exporter/main.go`, with kingpin and `--version` from `github.com/prometheus/common/version`.
- [x] `Makefile` with `build`, `test`, `lint` and `fmt` targets, injecting version, revision and date through `-ldflags` into the `prometheus/common/version` variables. The `integration` and `docker` targets come in phases 7 and 8, together with what they run.
- [x] `.golangci.yml` with the default linters plus `errorlint`, `gosec` and `revive`.
- [x] `.github/workflows/ci.yml` with `go test -race ./...` and golangci-lint.

### Phase 2: configuration (`internal/config`)

Status: [x] done

**Intent.** Load the YAML described under "Configuration" in the spec, apply the defaults and reject invalid configurations with clear messages.

**Expected outcome.** `config.Load(path)` returns a ready-to-use configuration, or an error listing every problem found, not just the first. Non-fatal warnings (interval shorter than a cycle) come back separately.

**Files**

- `internal/config/config.go`: `Config`, `CLI`, `Test` and `Server` types, and `Load`.
- `internal/config/size.go`: `Size` type with `UnmarshalYAML`, accepting `1024KiB`, `1MiB` or plain bytes.
- `internal/config/validate.go`: validation rules and warnings.
- `internal/config/config_test.go` and `internal/config/testdata/*.yml`.

**Tasks**

- [x] Types mirroring the spec's YAML. Durations as `time.Duration` (yaml.v3 already parses strings like `30m` and rejects integers). Library `go.yaml.in/yaml/v3`, the maintained continuation of `gopkg.in/yaml.v3`.
- [x] Decode with `yaml.Decoder.KnownFields(true)`, so a mistyped field is rejected instead of ignored.
- [x] Defaults: fill the struct with default values before decoding, then complete any empty server paths.
- [x] Every rule under "Validation" in the spec, plus whole seconds for `duration` and `http_timeout` and `upload_size` as a multiple of 1 KiB. Errors joined with `errors.Join`.
- [x] The warning for `interval` shorter than `servers × timeout` returned as `[]string`, without failing.
- [x] Table tests: minimal file (a single server) with defaults checked, full file, one case per validation rule, unknown field, duplicate server.

### Phase 3: running librespeed-cli (`internal/runner`)

Status: [x] done

**Intent.** Wrap everything about the `librespeed-cli` process: building the command line, running it, bounding its output, canceling it and parsing the JSON. This is where most failure cases live, so it carries most of the tests.

**Expected outcome.** `Runner.Run(ctx, Request{Test, Server, Timeout})` returns a `Result` in base units (bytes/s, seconds) or a `*RunError` with the exit code and the end of stderr. The `Result` always carries server, start and end, even on failure, so the scheduler can record the duration. `Runner.Version(ctx)` returns the binary's version. `runner.New(path)` looks the binary up in `PATH` when the path is empty.

**Files**

- `internal/runner/args.go`: `BuildArgs(test, server)` (exported so the scheduler can log the command line at `debug`) and `serverListJSON(server)`.
- `internal/runner/runner.go`: `Runner` type, `Run`, environment filtering, output limits.
- `internal/runner/report.go`: JSON report struct (only the fields used), parser, failure rules and unit conversion.
- `internal/runner/version.go`: `Runner.Version`.
- `internal/runner/cancel_unix.go` and `cancel_windows.go`: on Unix, `cmd.Cancel` sends `SIGTERM`; on Windows, which has no `SIGTERM`, the default kill is kept.
- `internal/runner/*_test.go` and `internal/runner/testdata/*.json`.

**Tasks**

- [x] `BuildArgs` producing the fixed flags and the spec's table of conditional flags. Table test covering every row.
- [x] `serverListJSON` with the current server as `id` 1, sent on stdin.
- [x] Process environment: only `PATH`, `HOME`, `SYSTEMROOT` and the proxy variables. An unexported `extraEnv` field lets tests pass variables to the fake binary.
- [x] Run with `exec.CommandContext`, an OS-specific `cmd.Cancel` and `cmd.WaitDelay = 5s`.
- [x] Stdout limited to 1 MiB: a writer that cancels the context and fails the run once past the limit. Stderr in a 4 KiB ring buffer.
- [x] Parser: an array with exactly one report; `null`, an empty array, invalid JSON and zero download or upload (with the phase enabled) are failures. Unknown fields are ignored.
- [x] Conversions to seconds and bytes per second, computed on the value in hundredths so the output has no floating point noise (0.03 ms is exactly `3e-05` s).
- [x] `RunError` with `ExitCode`, `Stderr` and `Err`, implementing `Unwrap`.
- [x] `Runner.Version`: runs `--version` with a 10s timeout and extracts the first version-like token (`v?\d+\.\d+\.\d+`) from the first line. If there is none, it returns the whole first line, so a custom CLI build still works.
- [x] Parser tests with fixtures: real v1.0.14 output against the `ghcr.io/librespeed/speedtest` container (full test, upload disabled and server down), `null`, `[]`, truncated JSON, extra fields, zero download, report without `tls`.
- [x] Execution tests with a fake binary through re-execution (`TestMain` checks an environment variable and, if set, acts as librespeed-cli). Cases: success, exit 1 with stderr, hang until the timeout, `SIGTERM` ignored (tests `WaitDelay`), output over 1 MiB, stdin received.

### Phase 4: metrics (`internal/collector`)

Status: [x] done

**Intent.** Keep per-server state and expose exactly the metrics listed under "Metrics" in the spec.

**Expected outcome.** A `prometheus.Collector` that builds constant metrics from a snapshot of the state. Removed servers disappear from the output without `DeleteLabelValues`.

**Files**

- `internal/collector/state.go`: `Collector` type with a mutex, created by `New(cfg)`, with `RecordSuccess(runner.Result)`, `RecordFailure(runner.Result)`, `SetRunning(bool)`, `SetConfig(*config.Config)` (servers, enabled phases and `collect_client_info`; drops the state of removed servers), `SetCLIVersion` and `SetReloadSuccessful`. The same type implements `prometheus.Collector`.
- `internal/collector/collector.go`: `Describe` and `Collect`.
- `internal/collector/collector_test.go` and `internal/collector/testdata/*.prom`.

**Tasks**

- [x] Per-server state: last successful result, `up`, timestamps, duration, test counters by result and byte counters by direction.
- [x] `Collect` emitting result metrics only after the first success, and only for enabled phases.
- [x] `librespeed_client_info` only with `collect_client_info: true`, and only when the server reported an IP or ISP.
- [x] `librespeed_server_info`, `librespeed_cli_info`, `librespeed_test_running`, `librespeed_config_last_reload_successful`.
- [x] Tests with `testutil.CollectAndCompare` against `.prom` files: empty initial state, after a success, success followed by failure (old values kept, `up` 0), upload disabled, phase re-enabled with no new test, server removed, result for an unknown server, client_info on and off. `testutil.CollectAndLint` checks names and types.

### Phase 5: scheduling (`internal/scheduler`)

Status: [x] done

**Intent.** Connect runner and collector as described under "Scheduling" in the spec: no overlapping cycles, servers in sequence, random delay and configuration changes at runtime.

**Expected outcome.** `Scheduler.Run(ctx)` blocks until the context is canceled. `Scheduler.Update(cfg, runner)` swaps configuration and runner together for the next cycle and resets the ticker if the interval changed. The runner is part of the update because a reload can change `librespeed_cli.path`.

**Files**

- `internal/scheduler/scheduler.go`
- `internal/scheduler/scheduler_test.go`

**Tasks**

- [x] Depend on the `TestRunner` (implemented by `*runner.Runner`) and `Recorder` (implemented by `*collector.Collector`) interfaces, so tests can use fakes.
- [x] Configuration and runner stored together in an `atomic.Pointer`. Each cycle reads the pointer once, at its start.
- [x] `run_on_start`, an `interval` ticker and a random delay of 0 to 10% of the interval before each cycle, except the `run_on_start` one, so the first result arrives quickly.
- [x] A tick that arrives during a cycle is dropped and logged at `warn`.
- [x] For each server: `SetRunning(true)`, `Run`, record success or failure, `SetRunning(false)`, and an `info` log line with the fields listed under "Logging" in the spec.
- [x] Canceling the context stops the current test and starts no further servers. A test stopped by shutdown does not count as a failure.
- [x] Tests with `testing/synctest`: immediate run with `run_on_start`, ticks dropped during a long cycle, server order, one server failing does not stop the others, interval change, cancellation mid-cycle.

### Phase 6: main binary and HTTP (`cmd/librespeed-exporter`)

Status: [x] done

**Intent.** Put everything together in one process: flags, configuration loading, librespeed-cli check, HTTP server, reload and shutdown.

**Expected outcome.** The exporter runs against a real LibreSpeed server and exposes the metrics. `kill -HUP` and `POST /-/reload` reload the configuration. `SIGTERM` exits within 5s even with a test in progress.

**Files**

- `cmd/librespeed-exporter/main.go`: flags, startup, signals.
- `cmd/librespeed-exporter/http.go`: the `/`, `/healthz` and `/-/reload` routes.
- `cmd/librespeed-exporter/reload.go`: reload logic shared by `SIGHUP` and the endpoint.
- `cmd/librespeed-exporter/http_test.go`

**Tasks**

- [x] Flags: `--config.file` (with `Envar("LIBRESPEED_EXPORTER_CONFIG")`), `--web.telemetry-path`, the exporter-toolkit flags (`kingpinflag.AddFlags` with default `:9469`) and the promslog flags.
- [x] Startup: load the config (exit on error), log warnings, create the runner and check `--version` (exit if the binary fails), and a dedicated registry with the collector plus the version, process and Go runtime collectors.
- [x] `librespeed_exporter_build_info` through client_golang's `collectors/version.NewCollector("librespeed_exporter")`, registered in `main`.
- [x] Landing page with exporter-toolkit's `web.NewLandingPage`.
- [x] `/healthz` returns 200; `/-/reload` only accepts `POST` (405 otherwise) and returns 500 with the error message when the reload fails.
- [x] Reload: `config.Load`; if `librespeed_cli.path` changed, create a new runner and check its version; then `collector.SetConfig`, `scheduler.Update` and `collector.SetReloadSuccessful`. Any error leaves the previous configuration untouched. Concurrent reloads are serialized by a mutex.
- [x] `signal.NotifyContext` for `SIGINT` and `SIGTERM`; on a signal, cancel the scheduler and call `Shutdown` on the HTTP server.
- [x] Handler tests with `httptest`: `/healthz`, `/metrics`, landing page, `/-/reload` methods, valid reload, invalid file keeps the previous configuration, CLI switch and broken CLI.
- [x] Manual test with the real CLI against the LibreSpeed container: metrics for an up and a down server, reload through `SIGHUP` and `POST`, `SIGTERM` during a test (exited in 0.12s, leaving no CLI process behind).

### Phase 7: integration test

Status: [x] done

**Intent.** Prove the exporter works with the real librespeed-cli against a real LibreSpeed server, and catch format changes when the CLI is upgraded.

**Expected outcome.** `make integration` starts a LibreSpeed server in a container, runs the exporter with the downloaded librespeed-cli and checks the metrics. CI runs this target in a separate job.

**Files**

- `scripts/fetch-librespeed-cli.sh`: downloads `librespeed-cli_<version>_<os>_<arch>.tar.gz` from the releases and verifies it against that release's `checksums.txt`. Also used by the Dockerfile in phase 8.
- `test/integration/integration_test.go` (build tag `integration`).
- `test/integration/compose.yml`: LibreSpeed server (`ghcr.io/librespeed/speedtest`).
- `integration` job in `.github/workflows/ci.yml`.

**Tasks**

- [x] Download script taking version and architecture as parameters, failing if the checksum does not match.
- [x] Test that starts the exporter against the container with `duration: 2s`, waits for the first cycle and checks `librespeed_up 1`, speeds and ping above zero, and that every metric in the spec is present.
- [x] Run `promtool check metrics` on the `/metrics` output (through the `prom/prometheus` image, in the `integration` target).
- [x] Record the real CLI JSON output as a phase 3 fixture (done in phase 3).
- [x] The test also checks that a down server has `up 0` and no speed metrics, and that the exporter exits with code 0 after `SIGTERM`.

### Phase 8: packaging

Status: [x] done

**Intent.** Ship the exporter as binaries, a container image and deployment examples, as described under "Packaging and distribution" in the spec.

**Expected outcome.** A `vX.Y.Z` tag publishes binaries and a multi-architecture image that includes librespeed-cli. The `deploy/` examples work as they are.

**Files**

- `Dockerfile`
- `.goreleaser.yaml`
- `.github/workflows/release.yml`
- `deploy/systemd/librespeed-exporter.service`
- `deploy/compose/docker-compose.yml`, `deploy/compose/prometheus.yml`, `deploy/compose/librespeed-exporter.yml`
- `deploy/prometheus/alerts.yml`
- `deploy/grafana/dashboard.json`

**Tasks**

- [x] Three-stage Dockerfile: exporter build, librespeed-cli download with the phase 7 script (mapping `TARGETARCH` and `TARGETVARIANT` to the asset names, e.g. `arm` + `v7` to `armv7`) and a final `gcr.io/distroless/static-debian13:nonroot` image. Cross builds checked for arm64 and armv7: both binaries come out static and for the right architecture. The CLI version comes from `ARG LIBRESPEED_CLI_VERSION=1.0.14`.
- [x] Copy the `LICENSE` from the librespeed-cli tarball to `/usr/share/licenses/librespeed-cli/`. Checked on v1.0.14: the linux/amd64 binary is static (fits the distroless image) and the tarball ships `LICENSE` next to the binary.
- [x] GoReleaser: linux (amd64, arm64, armv7), darwin (amd64, arm64) and windows (amd64) binaries, with checksums; `goreleaser release --snapshot` checked locally. The multi-architecture images on `ghcr.io/jansouza/librespeed-exporter` come from a separate job with `docker/build-push-action`, using the same Dockerfile as the local build.
- [x] systemd unit with `DynamicUser=yes` and the usual sandboxing options, including `SystemCallFilter=@system-service`. `systemd-analyze security` scores it 1.7; the exporter ran a full test under the same restrictions in a transient unit.
- [x] Example compose with the exporter, Prometheus, Grafana and a local LibreSpeed server. Checked end to end: target `up`, rules loaded, dashboard provisioned and every dashboard query valid.
- [x] The spec's alert rules in `deploy/prometheus/alerts.yml`, validated with `promtool check rules` in CI (`make check-deploy`).
- [x] Grafana dashboard with download and upload in bits/s, ping, jitter, `up` and data used per day.

### Phase 9: documentation and first release

Status: [ ] in progress (release pending)

**Intent.** Make the project usable by someone who never read the spec, and settle the open questions that block the release.

**Expected outcome.** A README with installation and a minimal configuration; tag `v0.1.0` published.

**Files**

- `README.md`
- `docs/usage.md`
- `librespeed-exporter.example.yml`
- `docs/spec.md` (update the open questions)

**Tasks**

- [x] README: what the exporter does, installation (container, binary plus librespeed-cli, systemd), minimal configuration, example `/metrics` output.
- [x] `docs/usage.md`: configuration reference, the bandwidth calculation, ICMP versus HTTP and the sysctl for ICMP in containers.
- [x] Example file with every field commented. A test makes sure it loads and documents the actual defaults.
- [x] Default port: keep `9469`. It is listed for script_exporter in the Prometheus port allocation wiki; kept on purpose and not registered.
- [x] Bytes or bits: bytes, following the Prometheus convention.
- [x] All documentation, the specification, this plan and the example configuration in English.
- [ ] Publish `v0.1.0`.

## Out of scope for this plan

Automatic server selection, probe mode and ping precision (the open questions in the spec) are left for after v1.
