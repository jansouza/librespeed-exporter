package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jansouza/librespeed-exporter/internal/collector"
	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/runner"
	"github.com/jansouza/librespeed-exporter/internal/scheduler"
)

// newRunnerFunc creates the runner for a configured librespeed-cli path and
// returns the CLI version. Replaced in tests.
type newRunnerFunc func(ctx context.Context, path string) (scheduler.TestRunner, string, error)

func newCLIRunner(ctx context.Context, path string) (scheduler.TestRunner, string, error) {
	r, err := runner.New(path)
	if err != nil {
		return nil, "", err
	}
	v, err := r.Version(ctx)
	if err != nil {
		return nil, "", err
	}
	return r, v, nil
}

// reloader applies a configuration file to the scheduler and collector. It is
// shared by SIGHUP and POST /-/reload, and serializes concurrent reloads.
type reloader struct {
	path      string
	logger    *slog.Logger
	collector *collector.Collector
	scheduler *scheduler.Scheduler
	newRunner newRunnerFunc

	mu      sync.Mutex
	cliPath string
	runner  scheduler.TestRunner
}

// reload loads the configuration file and applies it. On error nothing
// changes and the error is recorded in librespeed_config_last_reload_successful.
func (r *reloader) reload(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.apply(ctx); err != nil {
		r.collector.SetReloadSuccessful(false)
		r.logger.Error("Configuration reload failed", "err", err)
		return err
	}
	r.collector.SetReloadSuccessful(true)
	r.logger.Info("Configuration reloaded", "file", r.path)
	return nil
}

func (r *reloader) apply(ctx context.Context) error {
	cfg, warnings, err := config.Load(r.path)
	if err != nil {
		return err
	}

	run := r.runner
	if cfg.LibrespeedCLI.Path != r.cliPath {
		newRun, version, err := r.newRunner(ctx, cfg.LibrespeedCLI.Path)
		if err != nil {
			return fmt.Errorf("switching librespeed-cli: %w", err)
		}
		run = newRun
		r.collector.SetCLIVersion(version)
		r.logger.Info("Using librespeed-cli", "version", version)
	}

	for _, w := range warnings {
		r.logger.Warn(w)
	}
	r.cliPath = cfg.LibrespeedCLI.Path
	r.runner = run
	r.collector.SetConfig(cfg)
	r.scheduler.Update(cfg, run)
	return nil
}
