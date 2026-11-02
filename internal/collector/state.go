// Package collector keeps the outcome of the latest speed tests and exposes it
// as Prometheus metrics.
package collector

import (
	"sync"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/runner"
)

// Collector holds the latest test outcome per configured server and
// implements prometheus.Collector. It is safe for concurrent use.
type Collector struct {
	mu sync.RWMutex

	// order keeps the servers in configuration order, which is also the
	// order the scheduler tests them in.
	order   []string
	servers map[string]*serverState

	download          bool
	upload            bool
	collectClientInfo bool

	running          bool
	cliVersion       string
	reloadSuccessful bool
}

type serverState struct {
	url string

	tested       bool
	up           bool
	lastTest     time.Time
	lastDuration time.Duration

	// last is the latest successful result; lastSuccess is zero until there
	// is one. hasDownload and hasUpload tell which phases last measured.
	last        runner.Result
	lastSuccess time.Time
	hasDownload bool
	hasUpload   bool

	successes float64
	failures  float64
	bytesDown float64
	bytesUp   float64
}

// New returns a Collector for the servers in cfg.
func New(cfg *config.Config) *Collector {
	c := &Collector{servers: map[string]*serverState{}, reloadSuccessful: true}
	c.SetConfig(cfg)
	return c
}

// SetConfig applies a new configuration. State is kept for servers that are
// still configured, matched by name, and dropped for the others, so their
// series disappear.
func (c *Collector) SetConfig(cfg *config.Config) {
	c.mu.Lock()
	defer c.mu.Unlock()

	servers := make(map[string]*serverState, len(cfg.Servers))
	order := make([]string, 0, len(cfg.Servers))
	for _, srv := range cfg.Servers {
		st, ok := c.servers[srv.Name]
		if !ok {
			st = &serverState{}
		}
		st.url = srv.URL
		servers[srv.Name] = st
		order = append(order, srv.Name)
	}
	c.servers = servers
	c.order = order
	c.download = cfg.Test.Download
	c.upload = cfg.Test.Upload
	c.collectClientInfo = cfg.CollectClientInfo
}

// RecordSuccess stores the result of a successful test. Results for servers
// that are no longer configured are ignored.
func (c *Collector) RecordSuccess(res runner.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.servers[res.Server]
	if !ok {
		return
	}
	st.recordRun(res, true)
	st.last = res
	st.lastSuccess = res.FinishedAt
	st.hasDownload = c.download
	st.hasUpload = c.upload
	st.successes++
	st.bytesDown += float64(res.BytesReceived)
	st.bytesUp += float64(res.BytesSent)
}

// RecordFailure records a failed test. Only res.Server, res.StartedAt and
// res.FinishedAt are used; the previous successful result stays exposed.
func (c *Collector) RecordFailure(res runner.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.servers[res.Server]
	if !ok {
		return
	}
	st.recordRun(res, false)
	st.failures++
}

func (st *serverState) recordRun(res runner.Result, up bool) {
	st.tested = true
	st.up = up
	st.lastTest = res.FinishedAt
	st.lastDuration = res.Duration()
}

// SetRunning reports whether a librespeed-cli process is running.
func (c *Collector) SetRunning(running bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = running
}

// SetCLIVersion sets the librespeed-cli version exposed in librespeed_cli_info.
func (c *Collector) SetCLIVersion(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cliVersion = v
}

// SetReloadSuccessful records whether the last configuration reload worked.
func (c *Collector) SetReloadSuccessful(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloadSuccessful = ok
}
