package main

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
)

// newMux builds the HTTP routes: the landing page, metrics, health and
// reload.
func newMux(reg *prometheus.Registry, telemetryPath string, rl *reloader) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux.Handle(telemetryPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/-/reload", reloadHandler(rl))

	if telemetryPath != "/" {
		landing, err := web.NewLandingPage(web.LandingConfig{
			Name:        programName,
			Description: "Prometheus exporter for LibreSpeed speed tests",
			Version:     version.Info(),
			Links: []web.LandingLinks{
				{Address: telemetryPath, Text: "Metrics"},
				{Address: "/healthz", Text: "Health"},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("creating landing page: %w", err)
		}
		mux.Handle("/", landing)
	}
	return mux, nil
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprintln(w, "OK")
}

func reloadHandler(rl *reloader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Only POST is allowed.", http.StatusMethodNotAllowed)
			return
		}
		if err := rl.reload(r.Context()); err != nil {
			http.Error(w, "Reload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintln(w, "Configuration reloaded.")
	}
}
