package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"
)

// startupMargin is the time budgeted for everything in a librespeed-cli run
// other than the download and upload phases: process start, server check,
// IP lookup, ping and stream ramp-up.
const startupMargin = 15 * time.Second

// Validate checks the configuration and returns every problem found, joined
// into one error. Warnings describe settings that are allowed but probably
// not intended.
func (c *Config) Validate() (warnings []string, err error) {
	var errs []error
	fail := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", field, fmt.Sprintf(format, args...)))
	}

	if c.Interval <= 0 {
		fail("interval", "must be greater than zero")
	}
	if c.Timeout <= 0 {
		fail("timeout", "must be greater than zero")
	}

	t := c.Test
	checkWholeSeconds(fail, "test.duration", t.Duration)
	checkWholeSeconds(fail, "test.http_timeout", t.HTTPTimeout)
	if t.Concurrent < 1 {
		fail("test.concurrent", "must be at least 1, got %d", t.Concurrent)
	}
	if t.Chunks < 1 {
		fail("test.chunks", "must be at least 1, got %d", t.Chunks)
	}
	if t.UploadSize < KiB || t.UploadSize%KiB != 0 {
		fail("test.upload_size", "must be a positive multiple of 1KiB, got %s", t.UploadSize)
	}
	if t.PingMethod != PingHTTP && t.PingMethod != PingICMP {
		fail("test.ping_method", "must be %q or %q, got %q", PingHTTP, PingICMP, t.PingMethod)
	}
	if t.IPVersion != 0 && t.IPVersion != 4 && t.IPVersion != 6 {
		fail("test.ip_version", "must be 0, 4 or 6, got %d", t.IPVersion)
	}
	if t.SourceAddress != "" && net.ParseIP(t.SourceAddress) == nil {
		fail("test.source_address", "%q is not an IP address", t.SourceAddress)
	}
	if t.SourceAddress != "" && t.Interface != "" {
		fail("test", "source_address and interface cannot be set together")
	}

	phases := 0
	if t.Download {
		phases++
	}
	if t.Upload {
		phases++
	}
	if minTimeout := time.Duration(phases)*t.Duration + startupMargin; c.Timeout > 0 && c.Timeout < minTimeout {
		fail("timeout", "must be at least %s for the configured test phases, got %s", minTimeout, c.Timeout)
	}

	if len(c.Servers) == 0 {
		fail("servers", "at least one server must be configured")
	}
	seen := make(map[string]int, len(c.Servers))
	for i, s := range c.Servers {
		field := fmt.Sprintf("servers[%d]", i)
		if s.Name == "" {
			fail(field+".name", "must not be empty")
		} else if j, dup := seen[s.Name]; dup {
			fail(field+".name", "%q is already used by servers[%d]", s.Name, j)
		} else {
			seen[s.Name] = i
		}
		if err := checkServerURL(s.URL); err != nil {
			fail(field+".url", "%s", err)
		}
		if s.CAFile != "" {
			if _, err := os.Stat(s.CAFile); err != nil {
				fail(field+".ca_file", "%s", err)
			}
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	if cycle := time.Duration(len(c.Servers)) * c.Timeout; c.Interval < cycle {
		warnings = append(warnings, fmt.Sprintf(
			"interval (%s) is shorter than a worst-case cycle of %d server(s) × timeout (%s); ticks that arrive during a cycle will be skipped",
			c.Interval, len(c.Servers), cycle))
	}
	return warnings, nil
}

func checkWholeSeconds(fail func(field, format string, args ...any), field string, d time.Duration) {
	switch {
	case d < time.Second:
		fail(field, "must be at least 1s, got %s", d)
	case d%time.Second != 0:
		fail(field, "must be a whole number of seconds, got %s", d)
	}
}

func checkServerURL(raw string) error {
	if raw == "" {
		return errors.New("must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
}
