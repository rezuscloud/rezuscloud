package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsHAIngress_HassSource(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Hass-Source", "ingress")
	if !IsHAIngress(r) {
		t.Error("should detect HA ingress via X-Hass-Source")
	}
}

func TestIsHAIngress_HassURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Hass-URL", "http://hassio.local")
	if !IsHAIngress(r) {
		t.Error("should detect HA ingress via X-Hass-URL")
	}
}

func TestIsHAIngress_NoHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if IsHAIngress(r) {
		t.Error("should not detect HA ingress without headers")
	}
}

func TestMiddleware_RemovesFrameOptions(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
	})

	handler := Middleware(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Hass-Source", "ingress")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Header().Get("X-Frame-Options") != "" {
		t.Error("X-Frame-Options should be removed for HA ingress")
	}
}

func TestMiddleware_SetsCSP(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := Middleware(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Hass-Source", "ingress")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("CSP should be set for HA ingress")
	}
	if !contains(csp, "frame-ancestors *") {
		t.Error("CSP should allow frame-ancestors *")
	}
}

func TestMiddleware_NoHAHeaders(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.WriteHeader(http.StatusOK)
	})

	handler := Middleware(inner)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	// Should keep X-Frame-Options when NOT HA ingress.
	if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Error("X-Frame-Options should be kept for non-HA requests")
	}
}

func TestRelativeURL_Absolute(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"http://example.com/api/v1/tenants", "/api/v1/tenants"},
		{"https://example.com/tenants/prod", "/tenants/prod"},
		{"/api/v1/tenants", "/api/v1/tenants"},
		{"api/v1/tenants", "/api/v1/tenants"},
		{"http://localhost:8080/", "/"},
	}

	for _, tt := range tests {
		got := RelativeURL(tt.input)
		if got != tt.want {
			t.Errorf("RelativeURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
