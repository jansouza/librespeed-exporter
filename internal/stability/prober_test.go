package stability

import (
	"bytes"
	"context"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// pingServer is a fake LibreSpeed ping endpoint.
type pingServer struct {
	*httptest.Server
	hits    atomic.Int64
	status  atomic.Int64
	delay   atomic.Int64 // nanoseconds
	mu      sync.Mutex
	lastReq *http.Request
}

func newPingServer(t *testing.T, tlsServer bool) *pingServer {
	t.Helper()
	ps := &pingServer{}
	ps.status.Store(http.StatusOK)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		ps.mu.Lock()
		ps.lastReq = r.Clone(context.Background())
		ps.mu.Unlock()
		if d := time.Duration(ps.delay.Load()); d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(int(ps.status.Load()))
	})
	if tlsServer {
		ps.Server = httptest.NewTLSServer(handler)
	} else {
		ps.Server = httptest.NewServer(handler)
	}
	t.Cleanup(ps.Close)
	return ps
}

func (ps *pingServer) request() *http.Request {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.lastReq
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testConfig(servers ...config.Server) *config.Config {
	cfg := config.Default()
	cfg.Stability = config.Stability{Enabled: true, Interval: 10 * time.Millisecond, Timeout: time.Second}
	cfg.Servers = servers
	return &cfg
}

func server(name, url string) config.Server {
	return config.Server{Name: name, URL: url + "/", PingPath: config.DefaultPingPath}
}

type running struct {
	p    *Prober
	logs *syncBuffer
	stop func()
}

func start(t *testing.T, cfg *config.Config, before ...func(*Prober)) *running {
	t.Helper()
	logs := &syncBuffer{}
	p := New(cfg, slog.New(slog.NewTextHandler(logs, nil)))
	for _, f := range before {
		f(p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return &running{p: p, logs: logs, stop: stop}
}

func (r *running) requests(server, result string) float64 {
	return testutil.ToFloat64(r.p.metrics.requests.WithLabelValues(server, result))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func histogram(t *testing.T, vec *prometheus.HistogramVec, server string) *dto.Histogram {
	t.Helper()
	var m dto.Metric
	if err := vec.WithLabelValues(server).(prometheus.Histogram).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram()
}

func TestProbesRecordLatencyAndJitter(t *testing.T) {
	ps := newPingServer(t, false)
	ps.delay.Store(int64(5 * time.Millisecond))
	r := start(t, testConfig(server("home", ps.URL)))

	waitFor(t, "3 successful probes", func() bool { return r.requests("home", "success") >= 3 })
	r.stop()

	successes := r.requests("home", "success")
	lat := histogram(t, r.p.metrics.latency, "home")
	jit := histogram(t, r.p.metrics.jitter, "home")
	if float64(lat.GetSampleCount()) != successes {
		t.Errorf("latency samples = %d, successful probes = %v", lat.GetSampleCount(), successes)
	}
	if float64(jit.GetSampleCount()) != successes-1 {
		t.Errorf("jitter samples = %d, want %v", jit.GetSampleCount(), successes-1)
	}
	if mean := lat.GetSampleSum() / float64(lat.GetSampleCount()); mean < 0.005 {
		t.Errorf("mean latency %vs is below the 5ms server delay", mean)
	}
	if f := r.requests("home", "failure"); f != 0 {
		t.Errorf("failures = %v", f)
	}

	req := ps.request()
	if req.URL.Path != "/backend/empty.php" || req.URL.Query().Get("r") == "" {
		t.Errorf("request URL = %s, want the ping path with a random r parameter", req.URL)
	}
	if req.Header.Get("Cache-Control") != "no-cache" || !strings.HasPrefix(req.UserAgent(), "librespeed-exporter/") {
		t.Errorf("headers = %v", req.Header)
	}
}

func TestFailuresAreCountedAndLoggedOnTransitions(t *testing.T) {
	ps := newPingServer(t, false)
	ps.status.Store(http.StatusInternalServerError)
	r := start(t, testConfig(server("home", ps.URL)))

	waitFor(t, "3 failed probes", func() bool { return r.requests("home", "failure") >= 3 })
	if n := strings.Count(r.logs.String(), "Stability probe failing"); n != 1 {
		t.Errorf("failure logged %d times, want once:\n%s", n, r.logs)
	}

	ps.status.Store(http.StatusOK)
	waitFor(t, "recovery", func() bool { return strings.Contains(r.logs.String(), "Stability probe recovered") })
	r.stop()
	if histogram(t, r.p.metrics.latency, "home").GetSampleCount() == 0 {
		t.Error("no latency recorded after recovery")
	}
}

func TestTimeoutCountsAsFailure(t *testing.T) {
	ps := newPingServer(t, false)
	ps.delay.Store(int64(time.Second))
	cfg := testConfig(server("home", ps.URL))
	cfg.Stability.Timeout = 20 * time.Millisecond
	r := start(t, cfg)
	waitFor(t, "a timed out probe", func() bool { return r.requests("home", "failure") >= 1 })
}

func TestPauseStopsProbing(t *testing.T) {
	ps := newPingServer(t, false)
	r := start(t, testConfig(server("home", ps.URL)), func(p *Prober) { p.SetPaused(true) })

	time.Sleep(100 * time.Millisecond)
	if n := ps.hits.Load(); n != 0 {
		t.Fatalf("%d probes sent while paused", n)
	}
	r.p.SetPaused(false)
	waitFor(t, "a probe after resuming", func() bool { return ps.hits.Load() > 0 })
}

func TestDisabledProbesNothing(t *testing.T) {
	ps := newPingServer(t, false)
	cfg := testConfig(server("home", ps.URL))
	cfg.Stability.Enabled = false
	r := start(t, cfg)

	time.Sleep(50 * time.Millisecond)
	if n := ps.hits.Load(); n != 0 {
		t.Errorf("%d probes sent while disabled", n)
	}
	if n := testutil.CollectAndCount(r.p); n != 0 {
		t.Errorf("%d series exposed while disabled", n)
	}
}

func TestUpdateKeepsRemainingServersAndDropsOthers(t *testing.T) {
	a, b := newPingServer(t, false), newPingServer(t, false)
	r := start(t, testConfig(server("a", a.URL), server("b", b.URL)))
	waitFor(t, "probes to both servers", func() bool {
		return r.requests("a", "success") >= 1 && r.requests("b", "success") >= 1
	})

	before := r.requests("b", "success")
	r.p.Update(testConfig(server("b", b.URL)))
	if n := testutil.CollectAndCount(r.p, "librespeed_stability_requests_total"); n != 2 {
		t.Errorf("%d request series after removing a, want 2 (b success and failure)", n)
	}
	if after := r.requests("b", "success"); after < before {
		t.Errorf("b's counter went from %v to %v", before, after)
	}
	hitsA := a.hits.Load()
	time.Sleep(50 * time.Millisecond)
	if a.hits.Load() != hitsA {
		t.Error("removed server is still probed")
	}

	disabled := testConfig(server("b", b.URL))
	disabled.Stability.Enabled = false
	r.p.Update(disabled)
	if n := testutil.CollectAndCount(r.p); n != 0 {
		t.Errorf("%d series after disabling", n)
	}
}

func TestUpdateBeforeRunIsApplied(t *testing.T) {
	ps := newPingServer(t, false)
	disabled := testConfig(server("home", ps.URL))
	disabled.Stability.Enabled = false
	start(t, disabled, func(p *Prober) { p.Update(testConfig(server("home", ps.URL))) })
	waitFor(t, "a probe", func() bool { return ps.hits.Load() > 0 })
}

func TestIPVersionAndSourceAddress(t *testing.T) {
	ps := newPingServer(t, false) // listens on 127.0.0.1

	cfg := testConfig(server("home", ps.URL))
	cfg.Test.SourceAddress = "127.0.0.1"
	r := start(t, cfg)
	waitFor(t, "a probe from 127.0.0.1", func() bool { return r.requests("home", "success") >= 1 })
	r.stop()

	cfg = testConfig(server("home", ps.URL))
	cfg.Test.IPVersion = 6
	r = start(t, cfg)
	waitFor(t, "an IPv6-only probe to fail", func() bool { return r.requests("home", "failure") >= 1 })
	if r.requests("home", "success") != 0 {
		t.Error("IPv6-only probe reached an IPv4 address")
	}
}

func TestTLS(t *testing.T) {
	ps := newPingServer(t, true)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ps.Certificate().Raw})
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		server func(*config.Server)
		result string
	}{
		{"untrusted certificate", func(*config.Server) {}, "failure"},
		{"ca_file", func(s *config.Server) { s.CAFile = caFile }, "success"},
		{"insecure_skip_verify", func(s *config.Server) { s.InsecureSkipVerify = true }, "success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := server("home", ps.URL)
			tt.server(&srv)
			r := start(t, testConfig(srv))
			waitFor(t, "a "+tt.result, func() bool { return r.requests("home", tt.result) >= 1 })
		})
	}
}

func TestInvalidCAFileSkipsServer(t *testing.T) {
	ps := newPingServer(t, false)
	bad := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := server("broken", ps.URL)
	srv.CAFile = bad
	r := start(t, testConfig(srv, server("ok", ps.URL)))

	waitFor(t, "the valid server to be probed", func() bool { return r.requests("ok", "success") >= 1 })
	if !strings.Contains(r.logs.String(), "Stability probing disabled for server") {
		t.Errorf("invalid CA not logged:\n%s", r.logs)
	}
	if n := testutil.CollectAndCount(r.p, "librespeed_stability_requests_total"); n != 2 {
		t.Errorf("%d request series, want only the valid server's 2", n)
	}
}

func TestMetricsPassLint(t *testing.T) {
	ps := newPingServer(t, false)
	r := start(t, testConfig(server("home", ps.URL)))
	waitFor(t, "a probe", func() bool { return r.requests("home", "success") >= 1 })
	problems, err := testutil.CollectAndLint(r.p)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
}
