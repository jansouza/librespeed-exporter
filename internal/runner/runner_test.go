package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// The test binary doubles as a fake librespeed-cli: when fakeModeEnv is set,
// TestMain runs fakeCLI instead of the tests.
const (
	fakeModeEnv   = "FAKE_LIBRESPEED_CLI_MODE"
	fakeReportEnv = "FAKE_LIBRESPEED_CLI_REPORT"
	fakeRecordEnv = "FAKE_LIBRESPEED_CLI_RECORD"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(fakeCLI(mode))
	}
	os.Exit(m.Run())
}

// recording is what the fake CLI saw.
type recording struct {
	Args  []string
	Stdin string
	Env   []string
}

func fakeCLI(mode string) int {
	if dir := os.Getenv(fakeRecordEnv); dir != "" {
		stdin, _ := io.ReadAll(os.Stdin)
		data, _ := json.Marshal(recording{Args: os.Args[1:], Stdin: string(stdin), Env: os.Environ()})
		_ = os.WriteFile(filepath.Join(dir, "recording.json"), data, 0o600)
	}

	switch mode {
	case "report":
		data, err := os.ReadFile(os.Getenv(fakeReportEnv))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_, _ = os.Stdout.Write(data)
	case "exit1":
		fmt.Fprintln(os.Stderr, "Failed to get ping and jitter: connection refused")
		return 1
	case "long-stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 10000)+"END")
		return 1
	case "hang":
		time.Sleep(time.Minute)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
	case "flood":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("a"), 2*maxStdout))
	case "version":
		fmt.Print("librespeed-cli v1.0.14 (built on 2026-08-17T05:44:36Z)\nhttps://github.com/librespeed/speedtest-cli\n")
	case "version-dev":
		fmt.Println("librespeed-cli dev")
	case "silent":
	default:
		fmt.Fprintln(os.Stderr, "unknown fake mode", mode)
		return 2
	}
	return 0
}

func fakeRunner(t *testing.T, mode string, env ...string) *Runner {
	t.Helper()
	return &Runner{
		Path:      os.Args[0],
		extraEnv:  append([]string{fakeModeEnv + "=" + mode}, env...),
		waitDelay: 500 * time.Millisecond,
	}
}

func reportEnv(t *testing.T, fixture string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return fakeReportEnv + "=" + path
}

func defaultRequest() Request {
	return Request{Test: config.Default().Test, Server: testServer(), Timeout: 10 * time.Second}
}

func asRunError(t *testing.T, err error) *RunError {
	t.Helper()
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("error %v (%T) is not a *RunError", err, err)
	}
	return runErr
}

func TestRunSuccess(t *testing.T) {
	r := fakeRunner(t, "report", reportEnv(t, "report.json"))
	res, err := r.Run(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Server != "home" {
		t.Errorf("server = %q", res.Server)
	}
	if !approxEqual(res.DownloadBytesPerSecond, 19194.27e6/8) {
		t.Errorf("download = %v", res.DownloadBytesPerSecond)
	}
	if res.StartedAt.IsZero() || res.Duration() <= 0 {
		t.Errorf("timing not set: started %v, duration %v", res.StartedAt, res.Duration())
	}
}

func TestRunPassesArgsStdinAndFilteredEnv(t *testing.T) {
	t.Setenv("LIBRESPEED_EXPORTER_TEST_SECRET", "must-not-leak")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")

	dir := t.TempDir()
	r := fakeRunner(t, "report", reportEnv(t, "report.json"), fakeRecordEnv+"="+dir)
	req := defaultRequest()
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "recording.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec recording
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}

	if want := BuildArgs(req.Test, req.Server); !reflect.DeepEqual(rec.Args, want) {
		t.Errorf("args\n got: %q\nwant: %q", rec.Args, want)
	}
	wantStdin, _ := serverListJSON(req.Server)
	if rec.Stdin != string(wantStdin) {
		t.Errorf("stdin\n got: %s\nwant: %s", rec.Stdin, wantStdin)
	}
	if !slices.Contains(rec.Env, "HTTPS_PROXY=http://proxy.example:3128") {
		t.Errorf("HTTPS_PROXY not passed: %q", rec.Env)
	}
	for _, kv := range rec.Env {
		if strings.HasPrefix(kv, "LIBRESPEED_EXPORTER_TEST_SECRET=") {
			t.Errorf("unlisted variable passed to librespeed-cli: %s", kv)
		}
	}
}

func TestRunServerDown(t *testing.T) {
	r := fakeRunner(t, "report", reportEnv(t, "server-down.json"))
	res, err := r.Run(context.Background(), defaultRequest())
	if !errors.Is(err, ErrNoReport) {
		t.Fatalf("error = %v, want ErrNoReport", err)
	}
	if res.Server != "home" || res.FinishedAt.IsZero() {
		t.Errorf("failed result lacks server or timing: %+v", res)
	}
	if code := asRunError(t, err).ExitCode; code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRunNoOutput(t *testing.T) {
	_, err := fakeRunner(t, "silent").Run(context.Background(), defaultRequest())
	if err == nil || !strings.Contains(err.Error(), "nothing to stdout") {
		t.Fatalf("error = %v, want one about empty stdout", err)
	}
}

func TestRunExitCode(t *testing.T) {
	_, err := fakeRunner(t, "exit1").Run(context.Background(), defaultRequest())
	runErr := asRunError(t, err)
	if runErr.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", runErr.ExitCode)
	}
	if !strings.Contains(runErr.Stderr, "connection refused") {
		t.Errorf("stderr = %q", runErr.Stderr)
	}
	if !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("error message %q lacks the exit code", err)
	}
}

func TestRunKeepsStderrTail(t *testing.T) {
	_, err := fakeRunner(t, "long-stderr").Run(context.Background(), defaultRequest())
	// Under -cover the test binary appends a coverage warning after "END",
	// so only check that the start was dropped and the end kept.
	stderr := asRunError(t, err).Stderr
	if len(stderr) != stderrTail || !strings.HasPrefix(stderr, "xxx") || !strings.Contains(stderr, "END") {
		t.Errorf("stderr has %d bytes, starts with %q", len(stderr), stderr[:min(10, len(stderr))])
	}
}

func TestRunTimeout(t *testing.T) {
	req := defaultRequest()
	req.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := fakeRunner(t, "hang").Run(context.Background(), req)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if code := asRunError(t, err).ExitCode; code != -1 {
		t.Errorf("exit code = %d, want -1", code)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s after a 300ms timeout", elapsed)
	}
}

func TestRunKillsProcessIgnoringSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on Windows")
	}
	req := defaultRequest()
	req.Timeout = time.Second
	start := time.Now()
	_, err := fakeRunner(t, "ignore-term").Run(context.Background(), req)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	// Timeout plus the 500ms wait delay, with room for a slow machine.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Run took %s; the process was not killed after the wait delay", elapsed)
	}
}

func TestRunParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := fakeRunner(t, "hang").Run(ctx, defaultRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRunOutputTooLarge(t *testing.T) {
	_, err := fakeRunner(t, "flood").Run(context.Background(), defaultRequest())
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("error = %v, want ErrOutputTooLarge", err)
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		mode, want string
	}{
		{"version", "1.0.14"},
		{"version-dev", "librespeed-cli dev"},
	}
	for _, tt := range tests {
		got, err := fakeRunner(t, tt.mode).Version(context.Background())
		if err != nil {
			t.Fatalf("%s: Version: %v", tt.mode, err)
		}
		if got != tt.want {
			t.Errorf("%s: Version() = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestVersionErrors(t *testing.T) {
	for _, mode := range []string{"exit1", "silent"} {
		if _, err := fakeRunner(t, mode).Version(context.Background()); err == nil {
			t.Errorf("%s: expected an error", mode)
		}
	}
}

func TestResolvePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on the Unix executable bit")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, DefaultBinary)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir)
	if got, err := ResolvePath(""); err != nil || got != bin {
		t.Errorf("ResolvePath(\"\") = %q, %v; want %q", got, err, bin)
	}
	if got, err := ResolvePath(bin); err != nil || got != bin {
		t.Errorf("ResolvePath(%q) = %q, %v", bin, got, err)
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := ResolvePath(""); err == nil {
		t.Error("expected an error when librespeed-cli is not in PATH")
	}
	if _, err := New(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected an error for a missing binary")
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{size: 5}
	for _, s := range []string{"ab", "cd", "efg"} {
		_, _ = b.Write([]byte(s))
	}
	if got := b.String(); got != "cdefg" {
		t.Errorf("after small writes: %q", got)
	}
	_, _ = b.Write([]byte("0123456789"))
	if got := b.String(); got != "56789" {
		t.Errorf("after a large write: %q", got)
	}
}
