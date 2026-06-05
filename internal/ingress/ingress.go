// Package ingress provides middleware for Home Assistant ingress compatibility.
// HA ingress embeds the WebUI in an iframe with specific headers.
// This middleware detects HA ingress and adjusts security headers.
package ingress

import (
	"net/http"
	"strings"
)

// Headers that indicate Home Assistant ingress.
const (
	HeaderHassSource = "X-Hass-Source"
	HeaderHassURL    = "X-Hass-URL"
)

// Middleware adjusts response headers for HA ingress compatibility.
// - Removes X-Frame-Options: DENY (allows iframe embedding)
// - Sets permissive CSP for HA supervisor context
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsHAIngress(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Wrap ResponseWriter to strip X-Frame-Options from responses.
		wrapped := &haResponseWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
	})
}

// haResponseWriter strips X-Frame-Options and sets HA-compatible CSP.
type haResponseWriter struct {
	http.ResponseWriter
}

func (w *haResponseWriter) WriteHeader(code int) {
	w.ResponseWriter.Header().Del("X-Frame-Options")
	w.ResponseWriter.Header().Set("Content-Security-Policy", cspHAIngress())
	w.ResponseWriter.WriteHeader(code)
}

// IsHAIngress returns true if the request comes from Home Assistant ingress.
func IsHAIngress(r *http.Request) bool {
	return r.Header.Get(HeaderHassSource) != "" ||
		strings.HasPrefix(r.Header.Get(HeaderHassURL), "http")
}

// cspHAIngress returns a CSP policy compatible with HA ingress.
func cspHAIngress() string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' https://unpkg.com",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"frame-ancestors *",
	}, "; ")
}

// RelativeURL returns a relative URL (strips scheme and host).
// Ensures all URLs work through HA ingress proxy.
func RelativeURL(url string) string {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		parts := strings.SplitN(url, "/", 4)
		if len(parts) >= 4 {
			return "/" + parts[3]
		}
	}
	if !strings.HasPrefix(url, "/") {
		return "/" + url
	}
	return url
}
