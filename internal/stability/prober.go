// Package stability continuously probes the latency of each LibreSpeed server
// with small HTTP requests to its ping path, the equivalent of the LibreSpeed
// web stability test. It is independent of librespeed-cli, which has no such
// mode.
package stability

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// Prober runs one probe loop per configured server while stability probing
// is enabled. It implements prometheus.Collector and is safe for concurrent
// use.
type Prober struct {
	logger  *slog.Logger
	metrics *metrics
	paused  atomic.Bool

	mu      sync.Mutex
	cfg     *config.Config
	parent  context.Context // set by Run; nil before
	cancel  context.CancelFunc
	loops   sync.WaitGroup
	servers map[string]bool // servers whose series are exposed
}

// New returns a Prober for cfg. Call Run to start probing.
func New(cfg *config.Config, logger *slog.Logger) *Prober {
	return &Prober{logger: logger, metrics: newMetrics(), cfg: cfg, servers: map[string]bool{}}
}

// Run probes until ctx is canceled, then waits for the loops to stop.
func (p *Prober) Run(ctx context.Context) {
	p.mu.Lock()
	p.parent = ctx
	p.startLocked()
	p.mu.Unlock()

	<-ctx.Done()

	p.mu.Lock()
	p.stopLocked()
	p.mu.Unlock()
}

// Update applies a new configuration: the loops restart with it, and the
// series of servers that are no longer probed are dropped. Servers that stay
// keep their histograms and counters.
func (p *Prober) Update(cfg *config.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
	if p.parent == nil || p.parent.Err() != nil {
		return
	}
	p.stopLocked()
	p.startLocked()
}

// SetPaused stops (true) or resumes (false) probing, without stopping the
// loops. The scheduler pauses probing while a speed test saturates the link.
func (p *Prober) SetPaused(paused bool) {
	p.paused.Store(paused)
}

// Describe implements prometheus.Collector.
func (p *Prober) Describe(ch chan<- *prometheus.Desc) { p.metrics.describe(ch) }

// Collect implements prometheus.Collector.
func (p *Prober) Collect(ch chan<- prometheus.Metric) { p.metrics.collect(ch) }

func (p *Prober) startLocked() {
	active := map[string]bool{}
	if p.cfg.Stability.Enabled {
		ctx, cancel := context.WithCancel(p.parent)
		p.cancel = cancel
		st := p.cfg.Stability
		for _, srv := range p.cfg.Servers {
			c, err := newClient(srv, p.cfg.Test)
			if err != nil {
				p.logger.Error("Stability probing disabled for server", "server", srv.Name, "err", err)
				continue
			}
			active[srv.Name] = true
			p.metrics.init(srv.Name)
			p.loops.Go(func() { p.loop(ctx, c, srv.Name, st) })
		}
	}
	for name := range p.servers {
		if !active[name] {
			p.metrics.remove(name)
		}
	}
	p.servers = active
}

func (p *Prober) stopLocked() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.loops.Wait()
}

// loop probes one server every interval. A probe that takes longer than the
// interval is followed by the next one right away.
func (p *Prober) loop(ctx context.Context, c *client, server string, st config.Stability) {
	defer c.close()
	logger := p.logger.With("server", server)
	latency := p.metrics.latency.WithLabelValues(server)
	jitter := p.metrics.jitter.WithLabelValues(server)
	successes := p.metrics.requests.WithLabelValues(server, "success")
	failures := p.metrics.requests.WithLabelValues(server, "failure")

	var prev time.Duration // latency of the previous successful probe; 0 if none
	failing := false

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		start := time.Now()

		if p.paused.Load() {
			// Jitter is not measured across a pause.
			prev = 0
		} else {
			d, err := c.probe(ctx, st.Timeout)
			if ctx.Err() != nil {
				return
			}
			switch {
			case err != nil:
				failures.Inc()
				prev = 0
				if !failing {
					logger.Warn("Stability probe failing", "err", err)
					failing = true
				}
			default:
				successes.Inc()
				latency.Observe(d.Seconds())
				if prev > 0 {
					jitter.Observe((d - prev).Abs().Seconds())
				}
				prev = d
				if failing {
					logger.Info("Stability probe recovered")
					failing = false
				}
			}
		}

		timer.Reset(max(0, st.Interval-time.Since(start)))
	}
}
