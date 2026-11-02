// Command librespeed-exporter runs LibreSpeed speed tests on a schedule and
// exposes the results as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/common/promslog"
	promslogflag "github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/jansouza/librespeed-exporter/internal/collector"
	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/scheduler"
	"github.com/jansouza/librespeed-exporter/internal/stability"
)

const (
	programName    = "librespeed-exporter"
	defaultAddress = ":9469"
	// shutdownTimeout bounds the HTTP server shutdown. A running test is
	// stopped separately by the runner (SIGTERM, then SIGKILL after 5s).
	shutdownTimeout = 5 * time.Second
	// startupTimeout bounds the librespeed-cli --version check.
	startupTimeout = 15 * time.Second
)

func main() {
	os.Exit(run())
}

func run() int {
	app := kingpin.New(programName, "Prometheus exporter for LibreSpeed speed tests.")
	configFile := app.Flag("config.file", "Path to the configuration file.").
		Envar("LIBRESPEED_EXPORTER_CONFIG").Default("librespeed-exporter.yml").String()
	telemetryPath := app.Flag("web.telemetry-path", "Path under which to expose metrics.").
		Default("/metrics").String()
	webConfig := kingpinflag.AddFlags(app, defaultAddress)
	logConfig := &promslog.Config{}
	promslogflag.AddFlags(app, logConfig)
	app.Version(version.Print(programName))
	app.HelpFlag.Short('h')
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logger := promslog.New(logConfig)
	logger.Info("Starting "+programName, "version", version.Info())
	logger.Info("Build context", "build_context", version.BuildContext())

	cfg, warnings, err := config.Load(*configFile)
	if err != nil {
		logger.Error("Invalid configuration", "err", err)
		return 1
	}
	for _, w := range warnings {
		logger.Warn(w)
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), startupTimeout)
	testRunner, cliVersion, err := newCLIRunner(startCtx, cfg.LibrespeedCLI.Path)
	cancelStart()
	if err != nil {
		logger.Error("librespeed-cli is not usable", "err", err)
		return 1
	}
	logger.Info("Using librespeed-cli", "version", cliVersion)

	coll := collector.New(cfg)
	coll.SetCLIVersion(cliVersion)
	prober := stability.New(cfg, logger)
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		coll,
		prober,
		versioncollector.NewCollector("librespeed_exporter"),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)

	sched := scheduler.New(cfg, testRunner, pausingRecorder{coll, prober}, logger)
	rl := &reloader{
		path:      *configFile,
		logger:    logger,
		collector: coll,
		scheduler: sched,
		prober:    prober,
		newRunner: newCLIRunner,
		cliPath:   cfg.LibrespeedCLI.Path,
		runner:    testRunner,
	}

	mux, err := newMux(reg, *telemetryPath, rl)
	if err != nil {
		logger.Error("Setting up HTTP routes failed", "err", err)
		return 1
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	schedDone := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(schedDone)
	}()
	proberDone := make(chan struct{})
	go func() {
		prober.Run(ctx)
		close(proberDone)
	}()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				_ = rl.reload(ctx)
			}
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- web.ListenAndServe(srv, webConfig, logger)
	}()

	exitCode := 0
	select {
	case <-ctx.Done():
		logger.Info("Shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "err", err)
			exitCode = 1
		}
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", "err", err)
	}
	<-schedDone
	<-proberDone
	return exitCode
}

// pausingRecorder pauses stability probing while a speed test runs, so the
// saturated link does not show up as latency spikes.
type pausingRecorder struct {
	*collector.Collector
	prober *stability.Prober
}

func (r pausingRecorder) SetRunning(running bool) {
	r.prober.SetPaused(running)
	r.Collector.SetRunning(running)
}
