# Changelog

Notable changes to librespeed-exporter. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-11-02

### Added

- Stability probing, the equivalent of the LibreSpeed web stability test: with `stability.enabled: true`, each server's ping path gets a small HTTP request every `stability.interval` (default 1s). Off by default. Probing pauses while a speed test runs.
- Metrics `librespeed_stability_latency_seconds`, `librespeed_stability_jitter_seconds` and `librespeed_stability_requests_total`.
- `LibrespeedStabilityFailures` example alert.
- Dashboard: success rate, a latest-results table per server, markers on every chart where a speed test failed, and a stability row with latency percentiles, jitter, failed probes and a latency heatmap.
- The dashboard is generated from `deploy/grafana/dashboard.py` with `make dashboard`; CI checks that both match.
- Kubernetes section in the user guide.

### Changed

- Dashboard: download and upload, and ping and jitter, are now separate panels; speed axes start at zero; plain values are no longer colored red by Grafana's default thresholds; data used per day is shown as daily bars.
- The example Grafana data source sets `timeInterval: 1m` to match the example Prometheus scrape interval.

### Fixed

- Dashboard showed duplicate lines per server after a pod was replaced on Kubernetes. Gauge queries now aggregate by `server`.

## [0.1.0] - 2026-11-02

First release: scheduled speed tests through librespeed-cli, Prometheus metrics, configuration reload, container image with librespeed-cli, systemd unit, example compose stack, alert rules and Grafana dashboard.

[Unreleased]: https://github.com/jansouza/librespeed-exporter/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/jansouza/librespeed-exporter/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jansouza/librespeed-exporter/releases/tag/v0.1.0
