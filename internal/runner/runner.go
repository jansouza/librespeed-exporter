// Package runner runs librespeed-cli and turns its JSON report into a
// Result. It knows nothing about Prometheus.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// DefaultBinary is the name looked up in PATH when no path is configured.
const DefaultBinary = "librespeed-cli"

const (
	// maxStdout bounds what is read from librespeed-cli's stdout. A report
	// is well under 2 KiB; anything near this limit is not a report.
	maxStdout = 1 << 20
	// stderrTail is how much of the end of stderr is kept for error reports.
	stderrTail = 4 << 10
	// defaultWaitDelay is how long a process gets to exit after SIGTERM
	// before it is killed.
	defaultWaitDelay = 5 * time.Second
)

var (
	// ErrTimeout means the run took longer than Request.Timeout and the
	// process was stopped.
	ErrTimeout = errors.New("librespeed-cli run timed out")
	// ErrOutputTooLarge means librespeed-cli wrote more than 1 MiB to stdout.
	ErrOutputTooLarge = errors.New("librespeed-cli output exceeded 1 MiB")
)

// passEnv lists the environment variables passed on to librespeed-cli, so
// its behavior does not depend on where the exporter was started. SYSTEMROOT
// is needed by Go programs for networking on Windows.
var passEnv = []string{
	"PATH", "HOME", "SYSTEMROOT",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
}

// Request describes one test.
type Request struct {
	Test   config.Test
	Server config.Server
	// Timeout bounds the whole run. Zero means no limit beyond ctx.
	Timeout time.Duration
}

// Result is the outcome of one test. Server, StartedAt and FinishedAt are
// always set; the measurements are set only when Run returns no error.
type Result struct {
	Server     string
	StartedAt  time.Time
	FinishedAt time.Time

	DownloadBytesPerSecond float64
	UploadBytesPerSecond   float64
	PingSeconds            float64
	JitterSeconds          float64
	BytesReceived          uint64
	BytesSent              uint64
	ClientIP               string
	ISP                    string
}

// Duration is how long the librespeed-cli process ran.
func (r Result) Duration() time.Duration {
	return r.FinishedAt.Sub(r.StartedAt)
}

// RunError describes a failed run.
type RunError struct {
	// ExitCode is the process exit code, or -1 if it did not exit on its own.
	ExitCode int
	// Stderr is the last 4 KiB of the process stderr.
	Stderr string
	Err    error
}

func (e *RunError) Error() string {
	if e.ExitCode > 0 {
		return fmt.Sprintf("%v (exit code %d)", e.Err, e.ExitCode)
	}
	return e.Err.Error()
}

func (e *RunError) Unwrap() error { return e.Err }

// Runner runs librespeed-cli. Only one run should happen at a time, which is
// up to the caller.
type Runner struct {
	// Path is the librespeed-cli binary, as returned by ResolvePath.
	Path string

	// The fields below exist for tests.
	extraEnv  []string
	waitDelay time.Duration
}

// New returns a Runner for the binary at path, or for librespeed-cli in PATH
// when path is empty.
func New(path string) (*Runner, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	return &Runner{Path: resolved}, nil
}

// ResolvePath returns the binary to run: path itself, or librespeed-cli
// looked up in PATH when path is empty.
func ResolvePath(path string) (string, error) {
	if path == "" {
		path = DefaultBinary
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("finding librespeed-cli: %w", err)
	}
	return resolved, nil
}

// Run tests one server and returns the result. On failure the error is a
// *RunError.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	res := Result{Server: req.Server.Name}

	stdin, err := serverListJSON(req.Server)
	if err != nil {
		return res, &RunError{ExitCode: -1, Err: fmt.Errorf("encoding server list: %w", err)}
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, req.Timeout, ErrTimeout)
		defer cancel()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	stdout := &limitedBuffer{limit: maxStdout, overflow: func() { cancel(ErrOutputTooLarge) }}
	stderr := &tailBuffer{size: stderrTail}

	cmd := r.command(ctx, BuildArgs(req.Test, req.Server)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	res.StartedAt = time.Now()
	runErr := cmd.Run()
	res.FinishedAt = time.Now()

	fail := func(err error) (Result, error) {
		code := -1
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			code = cmd.ProcessState.ExitCode()
		}
		return res, &RunError{ExitCode: code, Stderr: stderr.String(), Err: err}
	}

	if ctx.Err() != nil {
		return fail(context.Cause(ctx))
	}
	if runErr != nil {
		return fail(fmt.Errorf("running librespeed-cli: %w", runErr))
	}
	if err := parseReport(stdout.Bytes(), req.Test, &res); err != nil {
		return fail(err)
	}
	return res, nil
}

// command builds an exec.Cmd for the binary with the filtered environment and
// graceful cancellation.
func (r *Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.Path, args...) // #nosec G204 -- the binary is chosen by the operator.
	cmd.Env = append(filteredEnv(), r.extraEnv...)
	setGracefulCancel(cmd)
	cmd.WaitDelay = r.waitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = defaultWaitDelay
	}
	return cmd
}

func filteredEnv() []string {
	var env []string
	for _, name := range passEnv {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// limitedBuffer keeps up to limit bytes. The first write past the limit calls
// overflow and fails, which stops the copy from the process.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow func()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow()
		return 0, ErrOutputTooLarge
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// tailBuffer keeps the last size bytes written to it.
type tailBuffer struct {
	buf  []byte
	size int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= b.size {
		b.buf = append(b.buf[:0], p[len(p)-b.size:]...)
		return n, nil
	}
	if over := len(b.buf) + len(p) - b.size; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
