package tfexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTofu writes a tiny shell script as the "tofu" binary. The script:
//   - echoes its argv to a marker file (so tests assert arg forwarding)
//   - dumps the child environment to a marker file (so tests assert env injection)
//   - honors $FAKE_SLEEP (seconds) to exercise the timeout path
//   - honors $FAKE_EXIT (exit code, default 0)
//   - honors $FAKE_STDOUT / $FAKE_STDERR to emit controlled output
//
// Returns the bin path plus the two marker file paths.
func fakeTofu(t *testing.T) (bin, argsFile, envFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tofu shell script is unix-only")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-tofu")
	argsFile = filepath.Join(dir, "args")
	envFile = filepath.Join(dir, "env")

	script := `#!/bin/sh
# Forward argv (skip $0).
{ for a in "$@"; do printf '%s\n' "$a"; done; } > "${FAKE_ARGS_FILE:-/dev/null}"
env | sort > "${FAKE_ENV_FILE:-/dev/null}"
[ -n "$FAKE_STDOUT" ] && printf '%s' "$FAKE_STDOUT"
[ -n "$FAKE_STDERR" ] && printf '%s' "$FAKE_STDERR" >&2
# Sleep via exec so SIGKILL from a context deadline reaches the sleeper directly.
# A plain sleep would run as an orphaned grandchild that keeps the inherited
# stdout/stderr pipes open, holding cmd.Wait() until WaitDelay — the exact
# hazard a real tofu+provider tree can hit. exec replaces the shell, so the
# killed PID owns the pipes and they close immediately.
# Real tofu+provider trees have the same shape; WaitDelay is the backstop.
if [ -n "$FAKE_SLEEP" ]; then exec sleep "$FAKE_SLEEP"; fi
exit "${FAKE_EXIT:-0}"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile, envFile
}

// newTestExec builds an Exec rooted at a temp dir, with the fake tofu binary and
// a marker-env bridge so the fake script can write its observations where the
// test can read them.
func newTestExec(t *testing.T) (e *Exec, bin, argsFile, envFile string) {
	t.Helper()
	bin, argsFile, envFile = fakeTofu(t)
	root := t.TempDir()
	ee, err := New(root,
		WithBinary(bin),
		WithTimeout(5*time.Second),
		// Bridge: tell the fake script where to write its markers + behavior.
		WithCredentials(func() map[string]string {
			return map[string]string{
				"FAKE_ARGS_FILE": argsFile,
				"FAKE_ENV_FILE":  envFile,
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return ee, bin, argsFile, envFile
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestNew_RequiresRoot(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty root")
	}
}

func TestNew_CreatesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tfwork")
	if _, err := New(root); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("root not created: %v", err)
	}
}

func TestNew_Defaults(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e.bin != "tofu" {
		t.Errorf("default bin = %q, want tofu", e.bin)
	}
	if e.timeout != DefaultTimeout {
		t.Errorf("default timeout = %v, want %v", e.timeout, DefaultTimeout)
	}
	if e.logf == nil {
		t.Error("default logger not set")
	}
}

func TestSanitizeTenant(t *testing.T) {
	cases := map[string]string{
		"personal":          "personal",
		"tenant-a_b":        "tenant-a_b",
		"tenant/with:slash": "tenant-with-slash",
		"":                  "default",
		"!!!":               "---",
		"UPPER123":          "UPPER123",
	}
	for in, want := range cases {
		if got := sanitizeTenant(in); got != want {
			t.Errorf("sanitizeTenant(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorkdir_CreatesDirAndBackend(t *testing.T) {
	e, err := New(t.TempDir(), WithBackendURL("http://127.0.0.1:8080/tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.Workdir("personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, backendFile)); err != nil {
		t.Fatalf("backend.tf not written: %v", err)
	}
	// Idempotent: a second call must not error.
	if _, err := e.Workdir("personal"); err != nil {
		t.Fatalf("second Workdir: %v", err)
	}
}

func TestWorkdir_BackendPointsAtTenantEndpoint(t *testing.T) {
	e, err := New(t.TempDir(), WithBackendURL("http://127.0.0.1:8080/tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.Workdir("personal")
	if err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(dir, backendFile))
	// Must be valid JSON and reference the tenant-keyed endpoint.
	if !strings.Contains(raw, `"address": "http://127.0.0.1:8080/tfstate?ID=personal"`) {
		t.Fatalf("backend address wrong:\n%s", raw)
	}
}

func TestWorkdir_NoBackendSkipsBackendFile(t *testing.T) {
	e, err := New(t.TempDir()) // no WithBackendURL
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.Workdir("personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, backendFile)); !os.IsNotExist(err) {
		t.Fatalf("backend.tf should not exist without backend URL, got err=%v", err)
	}
}

func TestRun_ForwardsArgsAndInjectsEnv(t *testing.T) {
	e, _, argsFile, envFile := newTestExec(t)
	// Add a real bootstrap-style credential to prove injection works.
	e.creds = func() map[string]string {
		return map[string]string{
			"FAKE_ARGS_FILE": argsFile,
			"FAKE_ENV_FILE":  envFile,
			"TF_VAR_token":   "super-secret-bootstrap",
		}
	}

	if _, err := e.Run(context.Background(), "personal", "init", "-backend=false"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Args forwarded verbatim (argv, $0 stripped by the fake).
	got := strings.TrimSpace(readFile(t, argsFile))
	if want := "init\n-backend=false"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	// Bootstrap credential injected into the child env.
	env := readFile(t, envFile)
	if !strings.Contains(env, "TF_VAR_token=super-secret-bootstrap") {
		t.Errorf("bootstrap cred not injected; env:\n%s", env)
	}
	// Automation marker + data dir scoped to the workdir present.
	if !strings.Contains(env, "TF_IN_AUTOMATION=1") {
		t.Errorf("TF_IN_AUTOMATION missing; env:\n%s", env)
	}
	if !strings.Contains(env, "TF_DATA_DIR=") {
		t.Errorf("TF_DATA_DIR missing; env:\n%s", env)
	}
}

func TestRun_ProcessEnvSecretsDoNotLeak(t *testing.T) {
	// RezusCloud's own process env must not pass through wholesale.
	t.Setenv("REZUSCLOUD_SUPER_SECRET", "leak-me-not")

	e, _, _, envFile := newTestExec(t)
	if _, err := e.Run(context.Background(), "personal", "version"); err != nil {
		t.Fatalf("run: %v", err)
	}
	env := readFile(t, envFile)
	if strings.Contains(env, "REZUSCLOUD_SUPER_SECRET") {
		t.Errorf("process secret leaked into child env:\n%s", env)
	}
}

func TestRun_CapturesStdoutAndStderr(t *testing.T) {
	e, _, _, envFile := newTestExec(t)
	e.creds = func() map[string]string {
		return map[string]string{
			"FAKE_ENV_FILE": envFile,
			"FAKE_STDOUT":   "plan: 1 to add\n",
			"FAKE_STDERR":   "warning: deprecation\n",
		}
	}

	res, err := e.Run(context.Background(), "personal", "plan")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Stdout, "plan: 1 to add") {
		t.Errorf("stdout not captured: %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "warning: deprecation") {
		t.Errorf("stderr not captured: %q", res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
}

func TestRun_NonZeroExitReturnsResultAndError(t *testing.T) {
	e, _, _, envFile := newTestExec(t)
	e.creds = func() map[string]string {
		return map[string]string{
			"FAKE_ENV_FILE": envFile,
			"FAKE_EXIT":     "3",
			"FAKE_STDERR":   "boom\n",
		}
	}

	res, err := e.Run(context.Background(), "personal", "apply")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if res == nil {
		t.Fatal("expected non-nil Result even on failure")
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Errorf("stderr not captured on failure: %q", res.Stderr)
	}
}

func TestRun_TimeoutKillsProcessAndSetsFlag(t *testing.T) {
	e, _, _, envFile := newTestExec(t)
	e.creds = func() map[string]string {
		return map[string]string{
			"FAKE_ENV_FILE": envFile,
			"FAKE_SLEEP":    "10", // sleeps far longer than the timeout below
		}
	}
	e.timeout = 100 * time.Millisecond

	start := time.Now()
	res, err := e.Run(context.Background(), "personal", "apply")
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if res == nil || !res.TimedOut {
		t.Fatal("expected TimedOut Result")
	}
	// Must return promptly after the deadline, not wait for the 10s sleep.
	if elapsed > 3*time.Second {
		t.Errorf("did not kill promptly: elapsed %v", elapsed)
	}
}

func TestRun_RespectsCallerContextDeadline(t *testing.T) {
	e, _, _, envFile := newTestExec(t)
	e.creds = func() map[string]string {
		return map[string]string{
			"FAKE_ENV_FILE": envFile,
			"FAKE_SLEEP":    "10",
		}
	}
	// A caller deadline must win even when e.timeout is longer.
	e.timeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := e.Run(ctx, "personal", "apply")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout (caller deadline)", err)
	}
}

func TestRun_StreamsLinesToLogger(t *testing.T) {
	e, _, _, envFile := newTestExec(t)
	e.creds = func() map[string]string {
		return map[string]string{
			"FAKE_ENV_FILE": envFile,
			"FAKE_STDOUT":   "line one\nline two\npartial-no-newline",
		}
	}
	var (
		mu   sync.Mutex
		logs []any
	)
	e.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, args...)
	}

	if _, err := e.Run(context.Background(), "personal", "plan"); err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := ""
	mu.Lock()
	for _, a := range logs {
		joined += "," + anyString(a)
	}
	mu.Unlock()
	if !strings.Contains(joined, "line one") || !strings.Contains(joined, "line two") {
		t.Errorf("complete lines not streamed:\n%s", joined)
	}
	if strings.Contains(joined, "partial-no-newline") {
		t.Errorf("partial line should not be logged until newline; got:\n%s", joined)
	}
}

func anyString(a any) string {
	switch v := a.(type) {
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func TestEnvCreds_PassesThroughOnlyNamedAndSet(t *testing.T) {
	t.Setenv("TF_VAR_one", "1")
	// TF_VAR_two deliberately unset.
	p := EnvCreds("TF_VAR_one", "TF_VAR_two")
	got := p()
	if len(got) != 1 || got["TF_VAR_one"] != "1" {
		t.Errorf("EnvCreds = %v, want only {TF_VAR_one:1}", got)
	}
}

func TestWithEncryption_FailFastOnShortPassphrase(t *testing.T) {
	// New must reject a too-short passphrase instead of silently running unencrypted.
	if _, err := New(t.TempDir(), WithEncryption("short")); err == nil {
		t.Fatal("expected error for short passphrase")
	}
	if _, err := New(t.TempDir(), WithEncryption("")); err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestWithEncryption_InjectsTFEncryptionEnv(t *testing.T) {
	// A valid passphrase builds the Exec; the fake-tofu marker env proves
	// TF_ENCRYPTION reached the subprocess carrying the pbkdf2 block. (The JSON
	// shape itself is tfencryption's own unit-test concern.)
	bin, _, envFile := fakeTofu(t)
	ee, err := New(t.TempDir(),
		WithBinary(bin),
		WithTimeout(5*time.Second),
		WithEncryption("correct-horse-battery-staple"),
		WithCredentials(func() map[string]string { return map[string]string{"FAKE_ENV_FILE": envFile} }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ee.Run(context.Background(), "personal", "version"); err != nil {
		t.Fatalf("run: %v", err)
	}
	env := readFile(t, envFile)
	if !strings.Contains(env, "TF_ENCRYPTION=") || !strings.Contains(env, "pbkdf2") {
		t.Errorf("TF_ENCRYPTION (pbkdf2) not injected into child env:\n%s", env)
	}
}
