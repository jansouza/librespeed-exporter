package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/promslog"

	"github.com/jansouza/librespeed-exporter/internal/collector"
	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/runner"
	"github.com/jansouza/librespeed-exporter/internal/scheduler"
	"github.com/jansouza/librespeed-exporter/internal/stability"
)

type nopRunner struct{}

func (nopRunner) Run(context.Context, runner.Request) (runner.Result, error) {
	return runner.Result{}, errors.New("not used")
}

const configA = "servers:\n  - name: a\n    url: http://a/\n"

type testEnv struct {
	path       string
	coll       *collector.Collector
	rl         *reloader
	server     *httptest.Server
	runnerArgs []string // paths passed to newRunner
	runnerErr  error
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{path: filepath.Join(t.TempDir(), "config.yml")}
	env.write(t, configA)
	cfg, _, err := config.Load(env.path)
	if err != nil {
		t.Fatal(err)
	}

	logger := promslog.NewNopLogger()
	env.coll = collector.New(cfg)
	env.coll.SetCLIVersion("1.0.14")
	env.rl = &reloader{
		path:      env.path,
		logger:    logger,
		collector: env.coll,
		scheduler: scheduler.New(cfg, nopRunner{}, env.coll, logger),
		prober:    stability.New(cfg, logger),
		newRunner: func(_ context.Context, path string) (scheduler.TestRunner, string, error) {
			env.runnerArgs = append(env.runnerArgs, path)
			if env.runnerErr != nil {
				return nil, "", env.runnerErr
			}
			return nopRunner{}, "2.0.0", nil
		},
		runner: nopRunner{},
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(env.coll)
	mux, err := newMux(reg, "/metrics", env.rl)
	if err != nil {
		t.Fatal(err)
	}
	env.server = httptest.NewServer(mux)
	t.Cleanup(env.server.Close)
	return env
}

func (env *testEnv) write(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(env.path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (env *testEnv) do(t *testing.T, method, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, env.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (env *testEnv) expectMetrics(t *testing.T, expected string, names ...string) {
	t.Helper()
	if err := testutil.CollectAndCompare(env.coll, strings.NewReader(expected), names...); err != nil {
		t.Error(err)
	}
}

const reloadOK = `
# HELP librespeed_config_last_reload_successful Whether the last configuration reload succeeded.
# TYPE librespeed_config_last_reload_successful gauge
librespeed_config_last_reload_successful 1
`

const reloadFailed = `
# HELP librespeed_config_last_reload_successful Whether the last configuration reload succeeded.
# TYPE librespeed_config_last_reload_successful gauge
librespeed_config_last_reload_successful 0
`

func TestHealthz(t *testing.T) {
	env := newTestEnv(t)
	if code, body := env.do(t, http.MethodGet, "/healthz"); code != http.StatusOK || body != "OK\n" {
		t.Errorf("GET /healthz = %d %q", code, body)
	}
}

func TestMetricsAndLandingPage(t *testing.T) {
	env := newTestEnv(t)
	if code, body := env.do(t, http.MethodGet, "/metrics"); code != http.StatusOK || !strings.Contains(body, `librespeed_server_info{server="a",url="http://a/"} 1`) {
		t.Errorf("GET /metrics = %d\n%s", code, body)
	}
	if code, body := env.do(t, http.MethodGet, "/"); code != http.StatusOK || !strings.Contains(body, programName) || !strings.Contains(body, `href="/metrics"`) {
		t.Errorf("GET / = %d\n%s", code, body)
	}
}

func TestReloadRequiresPOST(t *testing.T) {
	env := newTestEnv(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, env.server.URL+"/-/reload", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
		t.Errorf("GET /-/reload = %d, Allow %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

func TestReloadAppliesNewConfig(t *testing.T) {
	env := newTestEnv(t)
	env.write(t, "servers:\n  - name: b\n    url: http://b/\n")
	if code, body := env.do(t, http.MethodPost, "/-/reload"); code != http.StatusOK {
		t.Fatalf("POST /-/reload = %d %q", code, body)
	}
	env.expectMetrics(t, reloadOK+`
# HELP librespeed_server_info Configured LibreSpeed server, always 1.
# TYPE librespeed_server_info gauge
librespeed_server_info{server="b",url="http://b/"} 1
`, "librespeed_config_last_reload_successful", "librespeed_server_info")
	if len(env.runnerArgs) != 0 {
		t.Errorf("runner recreated although the CLI path did not change: %v", env.runnerArgs)
	}
}

func TestReloadInvalidConfigKeepsPrevious(t *testing.T) {
	env := newTestEnv(t)
	env.write(t, "servers: []\n")
	code, body := env.do(t, http.MethodPost, "/-/reload")
	if code != http.StatusInternalServerError || !strings.Contains(body, "at least one server") {
		t.Fatalf("POST /-/reload = %d %q", code, body)
	}
	env.expectMetrics(t, reloadFailed+`
# HELP librespeed_server_info Configured LibreSpeed server, always 1.
# TYPE librespeed_server_info gauge
librespeed_server_info{server="a",url="http://a/"} 1
`, "librespeed_config_last_reload_successful", "librespeed_server_info")

	// A later good reload clears the failure.
	env.write(t, configA)
	if code, _ := env.do(t, http.MethodPost, "/-/reload"); code != http.StatusOK {
		t.Fatalf("second reload = %d", code)
	}
	env.expectMetrics(t, reloadOK, "librespeed_config_last_reload_successful")
}

func TestReloadSwitchesCLI(t *testing.T) {
	env := newTestEnv(t)
	env.write(t, "librespeed_cli:\n  path: /opt/librespeed-cli\n"+configA)
	if code, body := env.do(t, http.MethodPost, "/-/reload"); code != http.StatusOK {
		t.Fatalf("POST /-/reload = %d %q", code, body)
	}
	if len(env.runnerArgs) != 1 || env.runnerArgs[0] != "/opt/librespeed-cli" {
		t.Errorf("newRunner calls = %v", env.runnerArgs)
	}
	env.expectMetrics(t, `
# HELP librespeed_cli_info Version of the librespeed-cli binary in use, always 1.
# TYPE librespeed_cli_info gauge
librespeed_cli_info{version="2.0.0"} 1
`, "librespeed_cli_info")

	// Reloading the same file again does not recreate the runner.
	if code, _ := env.do(t, http.MethodPost, "/-/reload"); code != http.StatusOK {
		t.Fatalf("second reload = %d", code)
	}
	if len(env.runnerArgs) != 1 {
		t.Errorf("newRunner calls = %v", env.runnerArgs)
	}
}

func TestReloadWithBrokenCLIKeepsPrevious(t *testing.T) {
	env := newTestEnv(t)
	env.runnerErr = errors.New("exec: not found")
	env.write(t, "librespeed_cli:\n  path: /missing\nservers:\n  - name: b\n    url: http://b/\n")
	code, body := env.do(t, http.MethodPost, "/-/reload")
	if code != http.StatusInternalServerError || !strings.Contains(body, "switching librespeed-cli") {
		t.Fatalf("POST /-/reload = %d %q", code, body)
	}
	env.expectMetrics(t, reloadFailed+`
# HELP librespeed_cli_info Version of the librespeed-cli binary in use, always 1.
# TYPE librespeed_cli_info gauge
librespeed_cli_info{version="1.0.14"} 1
# HELP librespeed_server_info Configured LibreSpeed server, always 1.
# TYPE librespeed_server_info gauge
librespeed_server_info{server="a",url="http://a/"} 1
`, "librespeed_config_last_reload_successful", "librespeed_cli_info", "librespeed_server_info")
}

func TestReloadEnablesStabilityAndSpeedTestsPauseIt(t *testing.T) {
	var hits atomic.Int64
	ping := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(ping.Close)

	env := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		env.rl.prober.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	env.write(t, "stability:\n  enabled: true\n  interval: 200ms\nservers:\n  - name: a\n    url: "+ping.URL+"/\n")
	if code, body := env.do(t, http.MethodPost, "/-/reload"); code != http.StatusOK {
		t.Fatalf("POST /-/reload = %d %q", code, body)
	}
	waitFor(t, func() bool { return hits.Load() > 0 })

	// The scheduler reports a running speed test through the recorder.
	rec := pausingRecorder{env.coll, env.rl.prober}
	rec.SetRunning(true)
	time.Sleep(300 * time.Millisecond) // let an in-flight probe finish
	paused := hits.Load()
	time.Sleep(500 * time.Millisecond)
	if hits.Load() != paused {
		t.Error("stability probes sent while a speed test runs")
	}
	rec.SetRunning(false)
	waitFor(t, func() bool { return hits.Load() > paused })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
