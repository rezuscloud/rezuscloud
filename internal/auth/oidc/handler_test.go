package oidc

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// flowHarness wires a real store + JWT manager + fake IdP into a Handler
// and serves it on its own test server (so redirect URIs resolve).
type flowHarness struct {
	idp     *testIdP
	handler *Handler
	server  *httptest.Server
	store   *state.Store
}

func newFlowHarness(t *testing.T, mutate func(*Config)) *flowHarness {
	t.Helper()
	idp := newTestIdP(t)
	store, err := state.Open(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jwtManager := auth.NewJWTManager("test-secret-please-ignore")

	cfg := Config{
		Issuer:       idp.issuer,
		ClientID:     "rezuscloud-test",
		ClientSecret: "test-secret",
		DefaultRole:  "view",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h, err := NewHandler(cfg, store, jwtManager)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	fh := &flowHarness{idp: idp, handler: h, store: store}
	fh.server = httptest.NewServer(mux)
	t.Cleanup(fh.server.Close)
	return fh
}

// begin starts a flow and returns the callback request values (state,
// nonce) plus the flow cookie for the follow-up request.
// noRedirect does not follow 3xx — the flow asserts on raw redirects.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func (fh *flowHarness) begin(t *testing.T) (*http.Response, flowState) {
	t.Helper()
	resp, err := noRedirect.Get(fh.server.URL + "/auth/oidc/login")
	if err != nil {
		t.Fatalf("GET login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", resp.StatusCode)
	}
	authURL, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := authURL.Query()
	if authURL.Host != hostOf(fh.idp.URL) || authURL.Path != "/auth" {
		t.Fatalf("redirect target = %s, want the IdP authorization endpoint", authURL)
	}
	if cc := q.Get("code_challenge_method"); cc != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 (PKCE)", cc)
	}
	if q.Get("code_challenge") == "" {
		t.Error("PKCE code_challenge missing")
	}

	var fs flowState
	for _, c := range resp.Cookies() {
		if c.Name != flowCookie {
			continue
		}
		blob, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil {
			t.Fatalf("decode flow cookie: %v", err)
		}
		if err := json.Unmarshal(blob, &fs); err != nil {
			t.Fatalf("parse flow cookie: %v", err)
		}
	}
	if fs.State == "" || fs.Nonce == "" || fs.Verifier == "" {
		t.Fatal("flow cookie missing state/nonce/verifier")
	}
	if q.Get("state") != fs.State || q.Get("nonce") != fs.Nonce {
		t.Error("redirect state/nonce must match the flow cookie")
	}
	return resp, fs
}

func (fh *flowHarness) callback(t *testing.T, fs flowState, qs url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fh.server.URL+"/auth/oidc/callback?"+qs.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: flowCookie, Value: encodeFlowState(t, fs)})
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	return resp
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

func encodeFlowState(t *testing.T, fs flowState) string {
	t.Helper()
	blob, err := json.Marshal(fs)
	if err != nil {
		t.Fatalf("marshal flow state: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(blob)
}

func TestFlow_FullLoginProvisionsAndSessions(t *testing.T) {
	fh := newFlowHarness(t, nil)

	_, fs := fh.begin(t)

	// The IdP mints the ID token bound to the flow's nonce.
	_ = fh.idp.mint(fs.Nonce, "sub-77", "erin", "erin@example.com", nil)
	qs := url.Values{"code": {"auth-code"}, "state": {fs.State}}
	resp := fh.callback(t, fs, qs)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("callback landed on %q, want /", loc)
	}
	var session string
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("callback must set the session cookie")
	}
	claims, err := fh.handler.jwtManager.ValidateToken(session)
	if err != nil {
		t.Fatalf("session JWT invalid: %v", err)
	}
	if claims.Username != "erin" {
		t.Errorf("session username = %q, want erin (internal JWT minting)", claims.Username)
	}

	// The user was provisioned with the default role and the identity linked.
	user, err := fh.store.GetUser("erin")
	if err != nil || user == nil {
		t.Fatalf("provisioned user missing: %v", err)
	}
	if user.Spec.Role != "view" {
		t.Errorf("role = %q, want default view", user.Spec.Role)
	}
}

func TestFlow_SecondLoginReusesIdentity(t *testing.T) {
	fh := newFlowHarness(t, nil)

	_, fs1 := fh.begin(t)
	_ = fh.idp.mint(fs1.Nonce, "sub-stable", "frank", "", nil)
	fh.callback(t, fs1, url.Values{"code": {"c1"}, "state": {fs1.State}})

	_, fs2 := fh.begin(t)
	_ = fh.idp.mint(fs2.Nonce, "sub-stable", "frank", "", nil)
	resp := fh.callback(t, fs2, url.Values{"code": {"c2"}, "state": {fs2.State}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("second callback status = %d", resp.StatusCode)
	}

	// Still exactly one identity resource and one user.
	ids, _, _, _, err := fh.store.ListResources(identityResourceType, state.ListOptions{})
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("identities = %d, want 1 (idempotent linking)", len(ids))
	}
}

func TestFlow_StateMismatchRejected(t *testing.T) {
	fh := newFlowHarness(t, nil)
	_, fs := fh.begin(t)
	_ = fh.idp.mint(fs.Nonce, "sub-x", "gina", "", nil)

	resp := fh.callback(t, fs, url.Values{"code": {"c"}, "state": {"forged"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Location"), "error=") {
		t.Errorf("must bounce to /login with an error, got %q", resp.Header.Get("Location"))
	}
	if _, err := fh.store.GetUser("gina"); err == nil {
		t.Log("user check skipped")
	}
	if u, _ := fh.store.GetUser("gina"); u != nil {
		t.Error("a failed flow must not provision users")
	}
}

func TestFlow_MissingFlowCookieRejected(t *testing.T) {
	fh := newFlowHarness(t, nil)
	_ = fh.idp.mint("no-nonce", "sub-y", "hank", "", nil)
	req, _ := http.NewRequest(http.MethodGet, fh.server.URL+"/auth/oidc/callback?code=c&state=s", nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Location"), "error=") {
		t.Error("callback without a flow cookie must be rejected")
	}
}

func TestFlow_DisabledHandlerRegistersNothing(t *testing.T) {
	// A disabled config yields a nil handler — wiring stays unconditional
	// and the mux simply has no /auth/oidc routes.
	h, err := NewHandler(Config{}, nil, nil)
	if err != nil || h != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", h, err)
	}
}
