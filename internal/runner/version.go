package runner

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const versionTimeout = 10 * time.Second

var versionPattern = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)

// Version runs librespeed-cli --version and returns the version it reports,
// without a leading "v". A build that prints no recognizable version yields
// the first line of its output.
func (r *Runner) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	stdout := &limitedBuffer{limit: 64 << 10, overflow: cancel}
	stderr := &tailBuffer{size: stderrTail}
	cmd := r.command(ctx, "--version")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running %s --version: %w: %s", r.Path, err, strings.TrimSpace(stderr.String()))
	}

	first, _, _ := strings.Cut(string(bytes.TrimSpace(stdout.Bytes())), "\n")
	first = strings.TrimSpace(first)
	if first == "" {
		return "", fmt.Errorf("%s --version printed nothing", r.Path)
	}
	if m := versionPattern.FindStringSubmatch(first); m != nil {
		return m[1], nil
	}
	return first, nil
}
