package tfexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/tfencryption"
)

// DefaultTimeout is applied to Run when the caller's context carries no
// deadline. A `tofu apply` against real cloud providers can take many minutes,
// so this is generous; callers that want tighter bounds pass their own
// deadline via the context.
const DefaultTimeout = 30 * time.Minute

// backendFile is the RezusCloud-owned backend config written into each tenant
// workdir. It points tofu's HTTP backend at RezusCloud's own /tfstate endpoint
// (#84) keyed by the tenant name, so every tofu command (init/plan/apply) reads
// and writes state through RezusCloud without per-command -backend-config flags.
// The `.tf.json` extension (not `.tf`) tells tofu to parse it as JSON.
const backendFile = "backend.tf.json"

// ErrTimeout is returned (and Result.TimedOut set) when Run exceeds its deadline.
var ErrTimeout = errors.New("tfexec: command timed out")

// Logf is the minimal logger signature the package depends on. It defaults to
// the standard library logger; callers (e.g. tests) can inject their own.
type Logf func(format string, args ...any)

// CredentialProvider returns environment-variable name→value pairs to inject
// into the tofu subprocess (bootstrap credentials + TF_VAR_* passthrough).
//
// It is a function, not a static map, so credentials can be refreshed between
// runs (e.g. re-read from a mounted secret that rotated). v1's default provider
// (EnvCreds) reads a configured set of names from the process environment.
type CredentialProvider func() map[string]string

// EnvCreds returns a CredentialProvider that copies the given env-var names
// from the process environment when they are set (empty values are skipped).
// This is the v1 bootstrap-credential provider: the operator sets the bootstrap
// secret set on the RezusCloud deployment, and EnvCreds passes them through to
// every tofu subprocess it spawns.
func EnvCreds(names ...string) CredentialProvider {
	return func() map[string]string {
		out := make(map[string]string, len(names))
		for _, n := range names {
			if v, ok := os.LookupEnv(n); ok && v != "" {
				out[n] = v
			}
		}
		return out
	}
}

// Exec orchestrates per-tenant `tofu` subprocess invocations. Construct one with
// New and drive it with Run. See the package doc for the concurrency contract.
type Exec struct {
	root         string             // tfwork root (e.g. $DATA_DIR/tfwork)
	backendURL   string             // RezusCloud's own /tfstate endpoint ("" ⇒ no backend.tf)
	bin          string             // path to the tofu binary
	creds        CredentialProvider // bootstrap credential injection (may be nil)
	tfEncryption string             // TF_ENCRYPTION env value ("" ⇒ encryption disabled)
	timeout      time.Duration      // default per-command timeout (0 ⇒ DefaultTimeout)
	logf         Logf               // output stream + lifecycle logger
}

// Option configures an Exec. Options may return an error to fail-fast during
// New (e.g. WithEncryption validates the passphrase). Existing no-failure
// options return nil.
type Option func(*Exec) error

// WithBackendURL sets the URL tofu writes state to. When non-empty, each tenant
// workdir gets a backend.tf pointing at "<url>?ID=<tenant>". When empty, no
// backend.tf is written (useful for `init -backend=false`-style runs and tests).
func WithBackendURL(url string) Option {
	return func(e *Exec) error {
		e.backendURL = strings.TrimRight(url, "/")
		return nil
	}
}

// WithBinary overrides the path to the tofu binary (default: "tofu" on PATH).
// Tests inject a fake binary here.
func WithBinary(path string) Option {
	return func(e *Exec) error {
		e.bin = path
		return nil
	}
}

// WithCredentials injects a bootstrap-credential provider.
func WithCredentials(c CredentialProvider) Option {
	return func(e *Exec) error {
		e.creds = c
		return nil
	}
}

// WithEncryption enables OpenTofu native state encryption (ADR 21, #86). The
// passphrase is validated and converted to a TF_ENCRYPTION env value via
// internal/tfencryption, then injected into every tofu subprocess so the
// passphrase NEVER touches disk (it lives only in RezusCloud's process and the
// transient tofu subprocess). With this set, state written via the #84 backend
// is opaque (encrypted) at rest and `StatePull` returns decrypted plaintext.
//
// Fail-fast: returns an error from New if the passphrase is missing or shorter
// than tfencryption.MinPassphraseLen, so a misconfigured deploy fails to start
// rather than silently producing unencrypted state.
func WithEncryption(passphrase string) Option {
	return func(e *Exec) error {
		cfg, err := tfencryption.Config(passphrase)
		if err != nil {
			return err
		}
		e.tfEncryption = cfg
		return nil
	}
}

// WithTimeout overrides the default per-command timeout applied when the
// caller's context carries no deadline.
func WithTimeout(d time.Duration) Option {
	return func(e *Exec) error {
		e.timeout = d
		return nil
	}
}

// WithLogger overrides the output/lifecycle logger (defaults to log.Printf).
func WithLogger(fn Logf) Option {
	return func(e *Exec) error {
		e.logf = fn
		return nil
	}
}

// New returns an Exec rooted at root (typically $DATA_DIR/tfwork), creating the
// root directory if needed.
func New(root string, opts ...Option) (*Exec, error) {
	if root == "" {
		return nil, errors.New("tfexec: root directory is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("tfexec: create root %q: %w", root, err)
	}
	e := &Exec{
		root:    root,
		bin:     "tofu",
		timeout: DefaultTimeout,
		logf:    log.Printf,
	}
	for _, o := range opts {
		if err := o(e); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Root returns the configured working-directory root.
func (e *Exec) Root() string { return e.root }

// Workdir returns (creating) the tofu working directory for a tenant and ensures
// the RezusCloud-owned backend.tf is present and current when a backend URL is
// configured. The directory is <root>/<sanitized-tenant>.
//
// The tenant name is sanitized to a filesystem-safe segment: only alphanumerics,
// '-' and '_' are kept; anything else becomes '-'. This keeps the workspace ID
// tofu uses for the backend lock in sync with the on-disk path.
func (e *Exec) Workdir(tenant string) (string, error) {
	seg := sanitizeTenant(tenant)
	dir := filepath.Join(e.root, seg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("tfexec: create workdir %q: %w", dir, err)
	}
	if e.backendURL == "" {
		return dir, nil
	}
	if err := e.writeBackend(dir, seg); err != nil {
		return "", err
	}
	return dir, nil
}

// writeBackend writes the RezusCloud-owned backend.tf into the workdir so tofu
// reads/writes state through RezusCloud's own HTTP backend keyed by the tenant.
// Rewriting on every call keeps it in sync if the backend URL rotates.
func (e *Exec) writeBackend(dir, tenant string) error {
	// Target shape: {"terraform":{"backend":{"http":{"address":"..."}}}}
	block := struct {
		Terraform map[string]map[string]map[string]string `json:"terraform"`
	}{
		Terraform: map[string]map[string]map[string]string{
			"backend": {
				"http": {
					"address": e.backendURL + "?ID=" + tenant,
				},
			},
		},
	}
	raw, err := json.MarshalIndent(block, "", "  ")
	if err != nil {
		return fmt.Errorf("tfexec: marshal backend block: %w", err)
	}
	path := filepath.Join(dir, backendFile)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("tfexec: write %s: %w", backendFile, err)
	}
	return nil
}

// Result is the structured outcome of one tofu command.
type Result struct {
	Tenant   string        `json:"tenant"`
	Args     []string      `json:"args"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	TimedOut bool          `json:"timed_out"`
}

// Run executes `tofu <args>` in the tenant's workdir.
//
// It builds a clean, augmented environment (TF_DATA_DIR scoped to the workdir,
// bootstrap credentials, TF_LOG), captures stdout/stderr into the Result while
// streaming them line-by-line to the logger tagged "[tfexec tenant=<t>]", and
// enforces a deadline: if the context has no deadline, the Exec's default
// timeout is applied. On overrun the process is killed and ErrTimeout returned.
//
// Run does not serialize per-tenant calls — see the package doc's concurrency
// contract.
func (e *Exec) Run(ctx context.Context, tenant string, args ...string) (*Result, error) {
	dir, err := e.Workdir(tenant)
	if err != nil {
		return nil, err
	}

	// Apply the default timeout only when the caller left the context open-ended.
	runCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok && e.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, e.timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, e.bin, args...)
	cmd.Dir = dir
	cmd.Env = e.buildEnv(dir)

	r := &Result{Tenant: tenant, Args: append([]string(nil), args...)}
	start := time.Now()

	// Tee output to both the captured Result fields and the live logger so
	// operators see tofu progress in real time AND we return the full text.
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = newLineTee(&stdoutBuf, e.logf, tenant, "stdout")
	cmd.Stderr = newLineTee(&stderrBuf, e.logf, tenant, "stderr")

	// WaitDelay bounds the wait after a kill so a defiant child can't hang Run.
	cmd.WaitDelay = 5 * time.Second

	waitErr := cmd.Run()
	r.Duration = time.Since(start)
	r.Stdout = stdoutBuf.String()
	r.Stderr = stderrBuf.String()
	r.ExitCode = cmd.ProcessState.ExitCode()

	switch {
	case errors.Is(waitErr, context.DeadlineExceeded), errors.Is(runCtx.Err(), context.DeadlineExceeded):
		r.TimedOut = true
		r.ExitCode = -1
		e.logf("[tfexec tenant=%s] command timed out after %s: %v", tenant, r.Duration, args)
		return r, fmt.Errorf("%w: tofu %s (tenant %s)", ErrTimeout, strings.Join(args, " "), tenant)
	case waitErr != nil:
		return r, fmt.Errorf("tfexec: tofu %s failed (tenant %s, exit %d): %w",
			strings.Join(args, " "), tenant, r.ExitCode, waitErr)
	default:
		e.logf("[tfexec tenant=%s] ok in %s: %s", tenant, r.Duration.Round(time.Millisecond), strings.Join(args, " "))
		return r, nil
	}
}

// StatePull runs `tofu state pull` and returns the state bytes from stdout.
// When encryption is configured (WithEncryption), tofu decrypts before writing
// to stdout, so the returned bytes are plaintext — the helper Phase 4's secrets
// cache uses to extract `client_configuration`. Without encryption configured,
// the raw (opaque) bytes are returned as-is.
//
// The tenant's workdir must already have a configured backend and initialized
// state (init + at least one apply, or a pushed state). The caller's context
// deadline is honored as in Run.
func (e *Exec) StatePull(ctx context.Context, tenant string) ([]byte, error) {
	r, err := e.Run(ctx, tenant, "state", "pull")
	if err != nil {
		return nil, err
	}
	return []byte(r.Stdout), nil
}

// buildEnv composes the child process environment: a known base (PATH, HOME,
// and a few essentials tofu/curl need) plus tofu-specific vars plus bootstrap
// credentials. It deliberately does NOT pass through os.Environ() wholesale, so
// RezusCloud's own process secrets never leak into the subprocess unless they
// are explicitly declared as bootstrap credentials.
func (e *Exec) buildEnv(dir string) []string {
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"LANG=" + envOrEnv("LANG", "C.UTF-8"),
		"TF_DATA_DIR=" + filepath.Join(dir, ".terraform"),
		"TF_IN_AUTOMATION=1",
		"TF_LOG=" + os.Getenv("TF_LOG"), // only set if the operator opted in
		"TF_CLI_ARGS=" + os.Getenv("TF_CLI_ARGS"),
	}
	if e.tfEncryption != "" {
		base = append(base, "TF_ENCRYPTION="+e.tfEncryption)
	}
	if e.creds != nil {
		for k, v := range e.creds() {
			base = append(base, k+"="+v)
		}
	}
	return base
}

func envOrEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// lineTee writes everything it receives to an underlying sink (the captured
// buffer) and, on each newline, emits the completed line to the logger.
type lineTee struct {
	sink    io.Writer
	logf    Logf
	tenant  string
	stream  string
	partial []byte
}

func newLineTee(sink io.Writer, logf Logf, tenant, stream string) *lineTee {
	return &lineTee{sink: sink, logf: logf, tenant: tenant, stream: stream}
}

func (t *lineTee) Write(p []byte) (int, error) {
	n, err := t.sink.Write(p) // capture first; never lose bytes on a logging failure
	if err != nil {
		return n, err
	}
	t.partial = append(t.partial, p...)
	for {
		i := bytesIndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		line := string(t.partial[:i])
		t.partial = t.partial[i+1:]
		// Drop a trailing '\r' so CRLF tofu output (rare) logs cleanly.
		line = strings.TrimRight(line, "\r")
		t.logf("[tfexec tenant=%s] %s: %s", t.tenant, t.stream, line)
	}
	return n, nil
}

// bytesIndexByte avoids importing bytes just for IndexByte.
func bytesIndexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// sanitizeTenant reduces an arbitrary tenant identifier to a single
// filesystem-safe segment used as both the workdir name and the backend lock ID.
func sanitizeTenant(tenant string) string {
	if tenant == "" {
		return "default"
	}
	var b strings.Builder
	b.Grow(len(tenant))
	for _, r := range tenant {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if s == "" {
		return "default"
	}
	return s
}
