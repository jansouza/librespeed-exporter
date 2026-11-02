package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/clientinfo"
	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/runner"
)

type call struct {
	server string
	at     time.Duration // since the start of the test
}

// fakeRunner records calls and takes `took` per test, or until canceled.
type fakeRunner struct {
	start time.Time
	took  time.Duration
	fail  map[string]error
	// client is the IP and ISP librespeed-cli reports, per server.
	client map[string]clientinfo.Info

	mu    sync.Mutex
	calls []call
}

func newFakeRunner(took time.Duration) *fakeRunner {
	return &fakeRunner{start: time.Now(), took: took, fail: map[string]error{}, client: map[string]clientinfo.Info{}}
}

func (f *fakeRunner) Run(ctx context.Context, req runner.Request) (runner.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{req.Server.Name, time.Since(f.start)})
	err := f.fail[req.Server.Name]
	client := f.client[req.Server.Name]
	f.mu.Unlock()

	res := runner.Result{Server: req.Server.Name, StartedAt: time.Now()}
	select {
	case <-time.After(f.took):
	case <-ctx.Done():
		res.FinishedAt = time.Now()
		return res, &runner.RunError{ExitCode: -1, Err: context.Cause(ctx)}
	}
	res.FinishedAt = time.Now()
	if err != nil {
		return res, err
	}
	res.DownloadBytesPerSecond = 1e6
	res.ClientIP, res.ISP = client.IP, client.ISP
	return res, nil
}

func (f *fakeRunner) Calls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

type fakeRecorder struct {
	mu        sync.Mutex
	successes []string
	failures  []string
	running   []bool
	results   []runner.Result // successful ones
}

func (r *fakeRecorder) RecordSuccess(res runner.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes = append(r.successes, res.Server)
	r.results = append(r.results, res)
}

func (r *fakeRecorder) RecordFailure(res runner.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, res.Server)
}

func (r *fakeRecorder) SetRunning(b bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = append(r.running, b)
}

func (r *fakeRecorder) snapshot() (successes, failures []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.successes...), append([]string(nil), r.failures...)
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

func testConfig(interval time.Duration, runOnStart bool, servers ...string) *config.Config {
	cfg := config.Default()
	cfg.Interval = interval
	cfg.RunOnStart = runOnStart
	for _, name := range servers {
		cfg.Servers = append(cfg.Servers, config.Server{Name: name, URL: "http://" + name + "/"})
	}
	return &cfg
}

// harness runs a Scheduler inside a synctest bubble.
type harness struct {
	s      *Scheduler
	runner *fakeRunner
	rec    *fakeRecorder
	logs   *syncBuffer
	cancel context.CancelFunc
	done   chan struct{}
}

func start(t *testing.T, cfg *config.Config, took time.Duration, opts ...func(*harness)) *harness {
	t.Helper()
	h := &harness{runner: newFakeRunner(took), rec: &fakeRecorder{}, logs: &syncBuffer{}, done: make(chan struct{})}
	logger := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.s = New(cfg, h.runner, h.rec, logger)
	h.s.jitter = func() float64 { return 0 }
	for _, opt := range opts {
		opt(h)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		h.s.Run(ctx)
		close(h.done)
	}()
	synctest.Wait()
	return h
}

func withJitter(v float64) func(*harness) {
	return func(h *harness) { h.s.jitter = func() float64 { return v } }
}

func (h *harness) stop() {
	h.cancel()
	<-h.done
}

func (h *harness) servers() []string {
	var names []string
	for _, c := range h.runner.Calls() {
		names = append(names, c.server)
	}
	return names
}

func TestRunOnStartTestsServersInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(time.Hour, true, "a", "b", "c"), time.Minute)
		time.Sleep(3 * time.Minute)
		synctest.Wait()

		want := []call{{"a", 0}, {"b", time.Minute}, {"c", 2 * time.Minute}}
		if got := h.runner.Calls(); !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %v, want %v", got, want)
		}
		if successes, _ := h.rec.snapshot(); !reflect.DeepEqual(successes, []string{"a", "b", "c"}) {
			t.Errorf("successes = %v", successes)
		}
		h.stop()
	})
}

func TestWithoutRunOnStartWaitsForInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(10*time.Minute, false, "a"), time.Second)
		time.Sleep(10*time.Minute - time.Second)
		synctest.Wait()
		if calls := h.runner.Calls(); len(calls) != 0 {
			t.Fatalf("tested before the first interval: %v", calls)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if want := []call{{"a", 10 * time.Minute}}; !reflect.DeepEqual(h.runner.Calls(), want) {
			t.Errorf("calls = %v, want %v", h.runner.Calls(), want)
		}
		h.stop()
	})
}

func TestJitterDelaysCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(10*time.Minute, false, "a"), time.Second, withJitter(0.5)) // 5% of 10m = 30s
		time.Sleep(10*time.Minute + 29*time.Second)
		synctest.Wait()
		if calls := h.runner.Calls(); len(calls) != 0 {
			t.Fatalf("tested before the jitter delay: %v", calls)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if want := []call{{"a", 10*time.Minute + 30*time.Second}}; !reflect.DeepEqual(h.runner.Calls(), want) {
			t.Errorf("calls = %v, want %v", h.runner.Calls(), want)
		}
		h.stop()
	})
}

func TestTicksDuringCycleAreSkipped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Each cycle takes 150s with a 60s interval: the ticks at 60s and
		// 120s fall inside the first cycle, the one at 180s starts the next.
		h := start(t, testConfig(time.Minute, true, "a"), 150*time.Second)
		time.Sleep(200 * time.Second)
		synctest.Wait()

		if want := []call{{"a", 0}, {"a", 180 * time.Second}}; !reflect.DeepEqual(h.runner.Calls(), want) {
			t.Errorf("calls = %v, want %v", h.runner.Calls(), want)
		}
		if n := strings.Count(h.logs.String(), "skipping this one"); n != 2 {
			t.Errorf("logged %d skipped ticks, want 2\n%s", n, h.logs)
		}
		h.stop()
	})
}

func TestFailureDoesNotStopOtherServers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(time.Hour, false, "a", "b"), time.Second, func(h *harness) {
			h.runner.fail["a"] = &runner.RunError{ExitCode: 1, Stderr: "boom\n", Err: errors.New("exit status 1")}
		})
		time.Sleep(time.Hour + time.Minute)
		synctest.Wait()

		successes, failures := h.rec.snapshot()
		if !reflect.DeepEqual(failures, []string{"a"}) || !reflect.DeepEqual(successes, []string{"b"}) {
			t.Errorf("failures = %v, successes = %v", failures, successes)
		}
		if logs := h.logs.String(); !strings.Contains(logs, "Test failed") || !strings.Contains(logs, "stderr=boom") {
			t.Errorf("failure not logged with stderr:\n%s", logs)
		}
		h.stop()
	})
}

func TestClientInfoLookup(t *testing.T) {
	looked := clientinfo.Info{IP: "203.0.113.8", ISP: "lookup ISP"}
	tests := []struct {
		name    string
		collect bool
		cli     clientinfo.Info
		err     error
		lookups int
		want    clientinfo.Info
	}{
		{name: "collect_client_info off", cli: clientinfo.Info{}, lookups: 0, want: clientinfo.Info{}},
		{name: "CLI reported the IP", collect: true, cli: clientinfo.Info{IP: "203.0.113.7", ISP: "cli ISP"}, lookups: 0,
			want: clientinfo.Info{IP: "203.0.113.7", ISP: "cli ISP"}},
		{name: "CLI reported nothing", collect: true, lookups: 1, want: looked},
		{name: "CLI reported only the ISP", collect: true, cli: clientinfo.Info{ISP: "cli ISP"}, lookups: 1,
			want: clientinfo.Info{IP: "203.0.113.8", ISP: "cli ISP"}},
		{name: "lookup failed", collect: true, err: clientinfo.ErrNotPublic, lookups: 1, want: clientinfo.Info{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := testConfig(time.Hour, true, "a")
				cfg.CollectClientInfo = tt.collect
				var lookups int
				h := start(t, cfg, time.Second, func(h *harness) {
					h.runner.client["a"] = tt.cli
					h.s.clientInfo = func(_ context.Context, srv config.Server, _ config.Test) (clientinfo.Info, error) {
						lookups++
						if srv.Name != "a" {
							t.Errorf("looked up server %q", srv.Name)
						}
						if tt.err != nil {
							return clientinfo.Info{}, tt.err
						}
						return looked, nil
					}
				})
				time.Sleep(time.Minute)
				synctest.Wait()

				h.rec.mu.Lock()
				results := append([]runner.Result(nil), h.rec.results...)
				h.rec.mu.Unlock()
				if len(results) != 1 {
					t.Fatalf("got %d successful results, want 1", len(results))
				}
				if got := (clientinfo.Info{IP: results[0].ClientIP, ISP: results[0].ISP}); got != tt.want {
					t.Errorf("recorded %+v, want %+v", got, tt.want)
				}
				if lookups != tt.lookups {
					t.Errorf("%d lookups, want %d", lookups, tt.lookups)
				}
				if tt.err != nil && !strings.Contains(h.logs.String(), "Client info lookup failed") {
					t.Errorf("failed lookup not logged:\n%s", h.logs)
				}
				h.stop()
			})
		})
	}
}

func TestUpdateChangesIntervalAndServers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(10*time.Minute, false, "a"), time.Second)
		time.Sleep(time.Minute)
		h.s.Update(testConfig(2*time.Minute, false, "b"), h.runner)
		synctest.Wait()

		// The ticker restarts at 1m with the new 2m interval.
		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()
		if want := []call{{"b", 3 * time.Minute}}; !reflect.DeepEqual(h.runner.Calls(), want) {
			t.Errorf("calls = %v, want %v", h.runner.Calls(), want)
		}
		if !strings.Contains(h.logs.String(), "Test interval changed") {
			t.Error("interval change not logged")
		}
		h.stop()
	})
}

func TestUpdateSwitchesRunner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig(time.Minute, false, "a")
		h := start(t, cfg, time.Second)
		other := newFakeRunner(time.Second)
		h.s.Update(cfg, other)
		time.Sleep(time.Minute + 2*time.Second)
		synctest.Wait()
		if len(h.runner.Calls()) != 0 || len(other.Calls()) != 1 {
			t.Errorf("old runner calls %v, new runner calls %v", h.runner.Calls(), other.Calls())
		}
		h.stop()
	})
}

func TestCancelStopsCycleWithoutRecordingFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(time.Hour, true, "a", "b"), time.Hour)
		time.Sleep(time.Minute)
		h.stop() // returns only after the running test has stopped

		if got := h.servers(); !reflect.DeepEqual(got, []string{"a"}) {
			t.Errorf("tested %v, want only a", got)
		}
		successes, failures := h.rec.snapshot()
		if len(successes)+len(failures) != 0 {
			t.Errorf("recorded successes %v, failures %v after shutdown", successes, failures)
		}
		if !strings.Contains(h.logs.String(), "Test canceled by shutdown") {
			t.Errorf("cancellation not logged:\n%s", h.logs)
		}
	})
}

func TestCancelDuringJitterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(10*time.Minute, false, "a"), time.Second, withJitter(0.9))
		time.Sleep(10*time.Minute + time.Second)
		h.stop()
		if calls := h.runner.Calls(); len(calls) != 0 {
			t.Errorf("tested during shutdown: %v", calls)
		}
	})
}

func TestRunningFlagWrapsEachTest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, testConfig(time.Hour, true, "a", "b"), time.Second)
		time.Sleep(3 * time.Second)
		synctest.Wait()
		h.rec.mu.Lock()
		running := append([]bool(nil), h.rec.running...)
		h.rec.mu.Unlock()
		if want := []bool{true, false, true, false}; !reflect.DeepEqual(running, want) {
			t.Errorf("SetRunning calls = %v, want %v", running, want)
		}
		if logs := h.logs.String(); !strings.Contains(logs, "args=\"--json --local-json -") {
			t.Errorf("command line not logged at debug level:\n%s", logs)
		}
		h.stop()
	})
}
