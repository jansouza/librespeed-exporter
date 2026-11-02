//go:build integration

// Package integration runs the exporter binary with the real librespeed-cli
// against a LibreSpeed server. Run it with make integration, which starts the
// server and downloads the CLI. Settings come from the environment:
//
//	LIBRESPEED_CLI         path to librespeed-cli (required)
//	LIBRESPEED_SERVER_URL  LibreSpeed server (default http://127.0.0.1:18080/)
//	METRICS_OUT            if set, the scraped metrics are written there
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestExporterAgainstLibreSpeed(t *testing.T) {
	cli := os.Getenv("LIBRESPEED_CLI")
	if cli == "" {
		t.Fatal("LIBRESPEED_CLI must point to a librespeed-cli binary")
	}
	serverURL := os.Getenv("LIBRESPEED_SERVER_URL")
	if serverURL == "" {
		serverURL = "http://127.0.0.1:18080/"
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "librespeed-exporter")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/librespeed-exporter")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the exporter: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(dir, "config.yml")
	cfg := fmt.Sprintf(`interval: 1h
timeout: 30s
librespeed_cli:
  path: %s
test:
  duration: 2s
servers:
  - name: local
    url: %s
  - name: down
    url: http://127.0.0.1:1/
`, cli, serverURL)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	addr := freeAddr(t)
	var logs bytes.Buffer
	cmd := exec.Command(bin, "--config.file="+cfgPath, "--web.listen-address="+addr)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if t.Failed() {
			t.Logf("exporter logs:\n%s", logs.String())
		}
	})

	// Both servers are tested on start; the cycle is over once both have
	// an up series.
	var families map[string]*dto.MetricFamily
	var raw []byte
	deadline := time.Now().Add(90 * time.Second)
	for {
		raw, families = scrape(t, "http://"+addr+"/metrics")
		if families != nil && len(families["librespeed_up"].GetMetric()) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no complete test cycle within 90s; last scrape:\n%s", raw)
		}
		time.Sleep(time.Second)
	}

	if out := os.Getenv("METRICS_OUT"); out != "" {
		if err := os.WriteFile(out, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := metrics(families)
	expect := func(name string, labels map[string]string, check func(float64) bool, desc string) {
		t.Helper()
		v, ok := m.get(name, labels)
		if !ok {
			t.Errorf("%s%v is missing", name, labels)
			return
		}
		if !check(v) {
			t.Errorf("%s%v = %v, want %s", name, labels, v, desc)
		}
	}
	positive := func(v float64) bool { return v > 0 }
	nonNegative := func(v float64) bool { return v >= 0 }
	equals := func(want float64) func(float64) bool { return func(v float64) bool { return v == want } }
	local := map[string]string{"server": "local"}
	down := map[string]string{"server": "down"}

	expect("librespeed_up", local, equals(1), "1")
	expect("librespeed_up", down, equals(0), "0")
	expect("librespeed_download_bytes_per_second", local, positive, "> 0")
	expect("librespeed_upload_bytes_per_second", local, positive, "> 0")
	expect("librespeed_ping_seconds", local, nonNegative, ">= 0")
	expect("librespeed_jitter_seconds", local, nonNegative, ">= 0")
	expect("librespeed_last_success_timestamp_seconds", local, positive, "> 0")
	expect("librespeed_last_test_timestamp_seconds", down, positive, "> 0")
	expect("librespeed_test_duration_seconds", local, positive, "> 0")
	expect("librespeed_tests_total", map[string]string{"server": "local", "result": "success"}, equals(1), "1")
	expect("librespeed_tests_total", map[string]string{"server": "down", "result": "failure"}, equals(1), "1")
	expect("librespeed_transferred_bytes_total", map[string]string{"server": "local", "direction": "download"}, positive, "> 0")
	expect("librespeed_transferred_bytes_total", map[string]string{"server": "local", "direction": "upload"}, positive, "> 0")
	expect("librespeed_test_running", nil, equals(0), "0")
	expect("librespeed_config_last_reload_successful", nil, equals(1), "1")
	expect("librespeed_server_info", map[string]string{"server": "local", "url": serverURL}, equals(1), "1")
	expect("librespeed_cli_info", nil, equals(1), "1")
	expect("librespeed_exporter_build_info", nil, equals(1), "1")
	if _, ok := m.get("librespeed_last_success_timestamp_seconds", down); ok {
		t.Error("librespeed_last_success_timestamp_seconds exposed for a server that never succeeded")
	}
	if _, ok := m.get("librespeed_download_bytes_per_second", down); ok {
		t.Error("download speed exposed for a server that never succeeded")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exporter exited with %v after SIGTERM", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("exporter did not exit within 10s of SIGTERM")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// scrape returns the raw body and parsed families, or nil families if the
// exporter is not answering yet.
func scrape(t *testing.T, url string) ([]byte, map[string]*dto.MetricFamily) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parsing /metrics: %v\n%s", err, raw)
	}
	return raw, families
}

type metrics map[string]*dto.MetricFamily

// get returns the value of the first series of name whose labels include all
// of want.
func (m metrics) get(name string, want map[string]string) (float64, bool) {
	for _, metric := range m[name].GetMetric() {
		labels := map[string]string{}
		for _, lp := range metric.GetLabel() {
			labels[lp.GetName()] = lp.GetValue()
		}
		matches := true
		for k, v := range want {
			if labels[k] != v {
				matches = false
			}
		}
		if !matches {
			continue
		}
		switch {
		case metric.Gauge != nil:
			return metric.GetGauge().GetValue(), true
		case metric.Counter != nil:
			return metric.GetCounter().GetValue(), true
		case metric.Untyped != nil:
			return metric.GetUntyped().GetValue(), true
		}
	}
	return 0, false
}
