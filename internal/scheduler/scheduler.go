// Package scheduler runs speed test cycles on an interval: one server at a
// time, never two cycles at once.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/clientinfo"
	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/runner"
)

// maxJitter is the largest random delay before a cycle, as a fraction of the
// interval.
const maxJitter = 0.1

// clientInfoTimeout bounds the client info lookup after a test.
const clientInfoTimeout = 10 * time.Second

// TestRunner runs one speed test. *runner.Runner implements it.
type TestRunner interface {
	Run(ctx context.Context, req runner.Request) (runner.Result, error)
}

// Recorder receives test outcomes. *collector.Collector implements it.
type Recorder interface {
	RecordSuccess(runner.Result)
	RecordFailure(runner.Result)
	SetRunning(bool)
}

// state is what a cycle reads at its start. It is replaced as a whole on
// Update, so a cycle never mixes two configurations.
type state struct {
	cfg    *config.Config
	runner TestRunner
}

// Scheduler runs test cycles until its context is canceled.
type Scheduler struct {
	recorder Recorder
	logger   *slog.Logger

	current atomic.Pointer[state]
	updated chan struct{}
	busy    atomic.Bool
	cycles  sync.WaitGroup

	// jitter returns a number in [0, 1); replaced in tests.
	jitter func() float64
	// clientInfo looks up the IP and ISP the server sees; replaced in tests.
	clientInfo func(context.Context, config.Server, config.Test) (clientinfo.Info, error)
}

// New returns a Scheduler for cfg. Call Run to start it.
func New(cfg *config.Config, r TestRunner, rec Recorder, logger *slog.Logger) *Scheduler {
	s := &Scheduler{
		recorder:   rec,
		logger:     logger,
		updated:    make(chan struct{}, 1),
		jitter:     rand.Float64,
		clientInfo: clientinfo.Fetch,
	}
	s.current.Store(&state{cfg: cfg, runner: r})
	return s
}

// Update replaces the configuration and runner used from the next cycle on.
// A cycle in progress finishes with the previous ones.
func (s *Scheduler) Update(cfg *config.Config, r TestRunner) {
	s.current.Store(&state{cfg: cfg, runner: r})
	select {
	case s.updated <- struct{}{}:
	default:
	}
}

// Run starts cycles every interval, plus one right away if run_on_start is
// set. It blocks until ctx is canceled and the cycle in progress, if any, has
// stopped.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.current.Load().cfg.Interval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer s.cycles.Wait()

	if s.current.Load().cfg.RunOnStart {
		// No jitter here: a freshly started exporter should report soon.
		s.startCycle(ctx, 0)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			delay := time.Duration(s.jitter() * maxJitter * float64(interval))
			s.startCycle(ctx, delay)
		case <-s.updated:
			if next := s.current.Load().cfg.Interval; next != interval {
				s.logger.Info("Test interval changed", "from", interval, "to", next)
				interval = next
				ticker.Reset(interval)
			}
		}
	}
}

// startCycle runs a cycle in the background after delay, unless one is
// already running.
func (s *Scheduler) startCycle(ctx context.Context, delay time.Duration) {
	if !s.busy.CompareAndSwap(false, true) {
		s.logger.Warn("Previous test cycle still running, skipping this one")
		return
	}
	s.cycles.Go(func() {
		defer s.busy.Store(false)
		if delay > 0 {
			t := time.NewTimer(delay)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
		s.cycle(ctx)
	})
}

// cycle tests every configured server in order.
func (s *Scheduler) cycle(ctx context.Context) {
	st := s.current.Load()
	for _, srv := range st.cfg.Servers {
		if ctx.Err() != nil {
			return
		}
		s.test(ctx, st, srv)
	}
}

func (s *Scheduler) test(ctx context.Context, st *state, srv config.Server) {
	req := runner.Request{Test: st.cfg.Test, Server: srv, Timeout: st.cfg.Timeout}
	logger := s.logger.With("server", srv.Name)
	logger.Debug("Starting test", "args", strings.Join(runner.BuildArgs(req.Test, srv), " "))

	s.recorder.SetRunning(true)
	res, err := st.runner.Run(ctx, req)
	s.recorder.SetRunning(false)

	if err != nil {
		// A test stopped by shutdown says nothing about the server.
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			logger.Info("Test canceled by shutdown")
			return
		}
		s.recorder.RecordFailure(res)
		attrs := []any{"duration", res.Duration().Round(time.Millisecond), "err", err}
		var runErr *runner.RunError
		if errors.As(err, &runErr) && runErr.Stderr != "" {
			attrs = append(attrs, "stderr", strings.TrimSpace(runErr.Stderr))
		}
		logger.Warn("Test failed", attrs...)
		return
	}

	if st.cfg.CollectClientInfo && res.ClientIP == "" {
		s.lookupClientInfo(ctx, st.cfg.Test, srv, &res, logger)
	}
	s.recorder.RecordSuccess(res)
	logger.Info("Test finished",
		"download_mbps", round2(res.DownloadBytesPerSecond*8/1e6),
		"upload_mbps", round2(res.UploadBytesPerSecond*8/1e6),
		"ping_ms", round2(res.PingSeconds*1000),
		"jitter_ms", round2(res.JitterSeconds*1000),
		"duration", res.Duration().Round(time.Millisecond),
		"bytes_received", res.BytesReceived,
		"bytes_sent", res.BytesSent,
	)
}

// lookupClientInfo asks the server's ip_path for the client IP, and the ISP
// if librespeed-cli had none. librespeed-cli reports no IP when the server
// does not look up the ISP, although the server still knows the IP. A failed
// lookup leaves the result as it is.
func (s *Scheduler) lookupClientInfo(ctx context.Context, test config.Test, srv config.Server, res *runner.Result, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, clientInfoTimeout)
	defer cancel()
	info, err := s.clientInfo(ctx, srv, test)
	if err != nil {
		logger.Debug("Client info lookup failed", "err", err)
		return
	}
	res.ClientIP = info.IP
	if res.ISP == "" {
		res.ISP = info.ISP
	}
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
