// Package settings implements the WebUI settings section.
//
// Extracted from the root web.Handler as part of issue #54 (WebUI Handler
// god-module split follow-up). Owns:
//
//   - GET  /settings                              — index page (operational config)
//   - GET  /settings/backups                      — backup snapshots + policy
//   - POST /settings/backups/database             — trigger database backup
//   - POST /settings/backups/resources            — trigger resources backup
//   - POST /settings/backups/restore              — restore from snapshot
//   - POST /settings/backups/policy               — update retention policy
//   - GET  /settings/users                        — users list (admin CRUD)
//   - POST /settings/users                        — create user (admin only)
//   - POST /settings/users/{name}                 — update user
//   - POST /settings/users/{name}/delete          — delete user
//   - GET  /settings/api-tokens                   — API tokens list + reveal
//   - POST /settings/users/{name}/api-tokens      — issue new token
//   - POST /settings/api-tokens/{id}/delete       — revoke token
//   - GET  /settings/audit                        — audit log with filters
//
// Also owns the settings-adjacent routes:
//
//   - GET  /providers                             — provider adapters table
//   - GET  /machines/join-manual                  — manual join helper
package settings

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/backup"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// Host is the subset of the root web.Handler that the settings section needs.
type Host interface {
	Render(w http.ResponseWriter, r *http.Request, props layout.BaseProps)
	PopToast(r *http.Request) layout.ToastData
	AuthRequired(next http.HandlerFunc) http.HandlerFunc
	CanMutate(r *http.Request) bool
	IsAdmin(r *http.Request) bool
	RedirectAction(w http.ResponseWriter, r *http.Request, target string)
	TenantNames() []string
	MachineLinkEndpoint() string
}

// Handler serves the settings + providers + manual-join routes.
type Handler struct {
	store      *state.Store
	backupSvc  *backup.Service // optional — nil disables backup routes
	auditStore audit.Store     // optional — nil disables the audit page
	host       Host
}

// New creates a settings Handler. backupSvc and auditStore may be nil —
// the corresponding pages degrade gracefully (503 / hidden).
func New(store *state.Store, backupSvc *backup.Service, auditStore audit.Store, host Host) *Handler {
	return &Handler{store: store, backupSvc: backupSvc, auditStore: auditStore, host: host}
}

// RegisterRoutes registers all settings + provider + manual-join routes,
// each gated by Host.AuthRequired.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	auth := h.host.AuthRequired

	// Settings index + sections.
	mux.HandleFunc("GET /settings", auth(h.SettingsIndexPage))
	mux.HandleFunc("GET /settings/backups", auth(h.BackupsPage))
	mux.HandleFunc("POST /settings/backups/database", auth(h.BackupsRunDatabase))
	mux.HandleFunc("POST /settings/backups/resources", auth(h.BackupsRunResources))
	mux.HandleFunc("POST /settings/backups/restore", auth(h.BackupsRestore))
	mux.HandleFunc("POST /settings/backups/policy", auth(h.BackupsPolicySave))
	mux.HandleFunc("GET /settings/users", auth(h.UsersPage))
	mux.HandleFunc("POST /settings/users", auth(h.UserCreate))
	mux.HandleFunc("POST /settings/users/{name}", auth(h.UserUpdate))
	mux.HandleFunc("POST /settings/users/{name}/delete", auth(h.UserDelete))
	mux.HandleFunc("GET /settings/api-tokens", auth(h.APITokensPage))
	mux.HandleFunc("POST /settings/users/{name}/api-tokens", auth(h.APITokenCreate))
	mux.HandleFunc("POST /settings/api-tokens/{id}/delete", auth(h.APITokenDelete))
	mux.HandleFunc("GET /settings/audit", auth(h.AuditPage))

	// Settings-adjacent.
	mux.HandleFunc("GET /providers", auth(h.ProvidersPage))
	mux.HandleFunc("GET /machines/join-manual", auth(h.ManualJoinPage))
}

// --- Settings index ---

// SettingsIndexPage renders /settings with section quick-links + a read-only
// operational config summary. Per ADR 17 this is minimal — no flag matrix.
func (h *Handler) SettingsIndexPage(w http.ResponseWriter, r *http.Request) {
	data := pages.SettingsIndexPageData{
		OperationalConfig: pages.OperationalConfig{
			JWTSessions:          envDefault("REZUSCLOUD_JWT_SESSIONS", "24h (default)"),
			BcryptCost:           envDefault("REZUSCLOUD_BCRYPT_COST", "12 (default)"),
			AuditRetentionDays:   envDefault("REZUSCLOUD_AUDIT_RETENTION_DAYS", "90 (default)"),
			BackupDirectory:      envDefault("REZUSCLOUD_BACKUP_DIR", "(tmpdir default)"),
			MachineLinkEndpoint:  envDefault("REZUSCLOUD_MACHINELINK_PUBLIC_ENDPOINT", "machinelink.rezus.cloud:50001"),
			ProviderGRPCEndpoint: envDefault("REZUSCLOUD_PROVIDER_PUBLIC_ENDPOINT", "provider.rezus.cloud:50190"),
		},
		ClusterSummary: pages.ClusterSummary{
			HTTPAddr:        envDefault("REZUSCLOUD_ADDR", ":8080"),
			MachineLinkAddr: envDefault("REZUSCLOUD_MACHINELINK_ADDR", ":50180"),
			ProviderAddr:    envDefault("REZUSCLOUD_PROVIDER_ADDR", ":50190"),
		},
		CanMutate: h.host.CanMutate(r),
	}

	h.host.Render(w, r, layout.BaseProps{
		Title:   "Settings",
		Page:    "settings",
		Content: pages.SettingsIndex(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// envDefault returns the env value if set + non-empty, else fallback.
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- Backups ---

func (h *Handler) backupServiceOrFail(w http.ResponseWriter, r *http.Request) (*backup.Service, bool) {
	if h.backupSvc == nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast="+url.QueryEscape("backup service unavailable")+"&toast-type=error")
		return nil, false
	}
	return h.backupSvc, true
}

// BackupsPage renders /settings/backups.
func (h *Handler) BackupsPage(w http.ResponseWriter, r *http.Request) {
	if h.backupSvc == nil {
		http.Error(w, "backup service unavailable", http.StatusServiceUnavailable)
		return
	}
	svc := h.backupSvc
	snapshots, _ := svc.ListSnapshots()
	policy, _ := svc.GetPolicy()

	lastSuccess := "never"
	failed := 0
	if len(snapshots) > 0 {
		for _, snap := range snapshots {
			if snap.Status.Status == "success" && lastSuccess == "never" {
				lastSuccess = snap.CreatedAt
			}
			if snap.Status.Status == "failed" {
				failed++
			}
		}
	}
	data := pages.BackupsPageData{
		Snapshots:   snapshots,
		Retention:   policy.Retention,
		LastSuccess: lastSuccess,
		Failures:    failed,
		RPOEstimate: rpoEstimate(lastSuccess),
		CanMutate:   h.host.CanMutate(r),
	}
	h.host.Render(w, r, layout.BaseProps{
		Title:   "Backups",
		Page:    "settings-backups",
		Content: pages.BackupsPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "Backups", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// BackupsRunDatabase triggers a database backup.
func (h *Handler) BackupsRunDatabase(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	svc, ok := h.backupServiceOrFail(w, r)
	if !ok {
		return
	}
	if _, err := svc.TriggerDatabase(r.Context()); err != nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/backups?toast=database+backup+created&toast-type=success")
}

// BackupsRunResources triggers a resources backup.
func (h *Handler) BackupsRunResources(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	svc, ok := h.backupServiceOrFail(w, r)
	if !ok {
		return
	}
	if _, err := svc.TriggerResources(r.Context()); err != nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/backups?toast=resources+backup+created&toast-type=success")
}

// BackupsRestore restores from a snapshot (optionally dry-run).
func (h *Handler) BackupsRestore(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast=invalid+restore+request&toast-type=error")
		return
	}
	snapshotID := strings.TrimSpace(r.FormValue("snapshotID"))
	dryRun := r.FormValue("dryRun") == "true"
	svc, ok := h.backupServiceOrFail(w, r)
	if !ok {
		return
	}
	result, err := svc.Restore(r.Context(), snapshotID, dryRun)
	if err != nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	msg := "restore applied"
	if dryRun {
		msg = "restore dry-run: " + strconv.Itoa(result.ResourcesSeen) + " resources"
	}
	h.host.RedirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(msg)+"&toast-type=success")
}

// BackupsPolicySave updates the backup retention policy.
func (h *Handler) BackupsPolicySave(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast=invalid+policy+form&toast-type=error")
		return
	}
	retentionStr := strings.TrimSpace(r.FormValue("retention"))
	retention, err := strconv.Atoi(retentionStr)
	if err != nil || retention <= 0 {
		h.host.RedirectAction(w, r, "/settings/backups?toast=retention+must+be+positive&toast-type=error")
		return
	}
	svc, ok := h.backupServiceOrFail(w, r)
	if !ok {
		return
	}
	if err := svc.UpdatePolicy(backup.Policy{Retention: retention}); err != nil {
		h.host.RedirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/backups?toast=retention+updated&toast-type=success")
}

// rpoEstimate renders a human-readable age string for the last successful
// backup. Used by the backups page.
func rpoEstimate(lastSuccess string) string {
	if lastSuccess == "" || lastSuccess == "never" {
		return "unknown"
	}
	t, err := time.Parse(time.RFC3339, lastSuccess)
	if err != nil {
		return "unknown"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return strconv.Itoa(int(d.Hours())) + "h"
}

// --- Users (W9) ---

// UsersPage renders /settings/users.
func (h *Handler) UsersPage(w http.ResponseWriter, r *http.Request) {
	isAdmin := h.host.IsAdmin(r)
	users, err := h.store.ListUsers()
	if err != nil {
		http.Error(w, "list users failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.UserRow, 0, len(users))
	for _, u := range users {
		row := pages.UserRow{Name: u.Metadata.Name, Role: u.Spec.Role}
		if u.Status.LastLogin != nil {
			row.LastLogin = u.Status.LastLogin.Format(time.RFC3339)
		} else {
			row.LastLogin = "—"
		}
		rows = append(rows, row)
	}

	h.host.Render(w, r, layout.BaseProps{
		Title: "Users",
		Page:  "users",
		Content: pages.UsersPage(pages.UsersPageData{
			Users:     rows,
			CanMutate: isAdmin,
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "Users", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// UserCreate handles POST /settings/users. Admin-only.
func (h *Handler) UserCreate(w http.ResponseWriter, r *http.Request) {
	if !h.host.IsAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast=invalid+form&toast-type=error")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	role := strings.TrimSpace(r.FormValue("role"))
	password := r.FormValue("password")
	if name == "" || !auth.ValidRoles[role] || password == "" {
		h.host.RedirectAction(w, r, "/settings/users?toast=name,+role,+password+required&toast-type=error")
		return
	}
	if existing, _ := h.store.GetUser(name); existing != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast=user+already+exists&toast-type=error")
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if _, err := h.store.CreateUser(name, state.UserSpec{Role: role, PasswordHash: hash}); err != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/users?toast=user+created&toast-type=success")
}

// UserUpdate handles POST /settings/users/{name}.
func (h *Handler) UserUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.host.IsAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast=invalid+form&toast-type=error")
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	password := r.FormValue("password")
	if !auth.ValidRoles[role] {
		h.host.RedirectAction(w, r, "/settings/users?toast=invalid+role&toast-type=error")
		return
	}
	existing, err := h.store.GetUser(name)
	if err != nil || existing == nil {
		h.host.RedirectAction(w, r, "/settings/users?toast=user+not+found&toast-type=error")
		return
	}
	hash := existing.Spec.PasswordHash
	if password != "" {
		hash, err = auth.HashPassword(password)
		if err != nil {
			h.host.RedirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
			return
		}
	}
	if _, err := h.store.UpdateUser(name, existing.Metadata.ResourceVersion, state.UserSpec{Role: role, PasswordHash: hash}); err != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/users?toast=user+updated&toast-type=success")
}

// UserDelete handles POST /settings/users/{name}/delete.
func (h *Handler) UserDelete(w http.ResponseWriter, r *http.Request) {
	if !h.host.IsAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if name == auth.UserFromContext(r.Context()) {
		h.host.RedirectAction(w, r, "/settings/users?toast=cannot+delete+current+user&toast-type=error")
		return
	}
	if err := h.store.DeleteUser(name); err != nil {
		h.host.RedirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/users?toast=user+deleted&toast-type=success")
}

// --- API Tokens (W9) ---

// APITokensPage renders /settings/api-tokens with the one-time reveal flow.
func (h *Handler) APITokensPage(w http.ResponseWriter, r *http.Request) {
	caller := auth.UserFromContext(r.Context())
	isAdmin := h.host.IsAdmin(r)

	userName := ""
	if !isAdmin {
		userName = caller
	}
	tokens, err := h.store.ListAPITokens(userName)
	if err != nil {
		http.Error(w, "list tokens failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.APITokenRow, 0, len(tokens))
	now := time.Now().UTC()
	for _, t := range tokens {
		row := pages.APITokenRow{
			ID:        t.ID,
			UserName:  t.UserName,
			CreatedAt: t.CreatedAt.Format(time.RFC3339),
			LastUsed:  "—",
			ExpiresAt: "never",
			Status:    "active",
		}
		if u, _ := h.store.GetUser(t.UserName); u != nil {
			row.Role = u.Spec.Role
		}
		if t.LastUsed != nil {
			row.LastUsed = t.LastUsed.Format(time.RFC3339)
		}
		if t.ExpiresAt != nil {
			row.ExpiresAt = t.ExpiresAt.Format(time.RFC3339)
			if now.After(*t.ExpiresAt) {
				row.Status = "expired"
			}
		}
		rows = append(rows, row)
	}

	data := pages.APITokensPageData{
		Tokens:      rows,
		CanMutate:   h.host.CanMutate(r),
		CurrentUser: caller,
	}

	if revealCookie, _ := r.Cookie("rezuscloud_token_reveal"); revealCookie != nil {
		parts := strings.SplitN(revealCookie.Value, "|", 3)
		if len(parts) >= 2 {
			data.NewTokenID = parts[0]
			data.NewSecret = parts[1]
			if len(parts) == 3 {
				data.NewExpiresAt = parts[2]
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name: "rezuscloud_token_reveal", Value: "", Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
	}

	h.host.Render(w, r, layout.BaseProps{
		Title:   "API Tokens",
		Page:    "api-tokens",
		Content: pages.APITokensPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "API Tokens", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// APITokenCreate issues a token and stages the one-time reveal cookie.
func (h *Handler) APITokenCreate(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	target := r.PathValue("name")
	caller := auth.UserFromContext(r.Context())
	if !h.host.IsAdmin(r) && caller != target {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	user, err := h.store.GetUser(target)
	if err != nil || user == nil {
		h.host.RedirectAction(w, r, "/settings/api-tokens?toast=user+not+found&toast-type=error")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.host.RedirectAction(w, r, "/settings/api-tokens?toast=invalid+form&toast-type=error")
		return
	}
	days, _ := strconv.Atoi(r.FormValue("expiresInDays"))
	var expiresAt *time.Time
	if days > 0 {
		t := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
		expiresAt = &t
	}
	plaintext, id, hash, err := auth.GenerateAPIToken()
	if err != nil {
		h.host.RedirectAction(w, r, "/settings/api-tokens?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if _, err := h.store.CreateAPIToken(id, target, hash, expiresAt); err != nil {
		h.host.RedirectAction(w, r, "/settings/api-tokens?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	expires := ""
	if expiresAt != nil {
		expires = expiresAt.Format(time.RFC3339)
	}
	http.SetCookie(w, &http.Cookie{
		Name: "rezuscloud_token_reveal", Value: id + "|" + plaintext + "|" + expires,
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300,
	})
	h.host.RedirectAction(w, r, "/settings/api-tokens?toast=token+created&toast-type=success")
}

// APITokenDelete revokes a token.
func (h *Handler) APITokenDelete(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	tok, err := h.store.GetAPIToken(id)
	if err != nil || tok == nil {
		h.host.RedirectAction(w, r, "/settings/api-tokens?toast=token+not+found&toast-type=error")
		return
	}
	caller := auth.UserFromContext(r.Context())
	if !h.host.IsAdmin(r) && caller != tok.UserName {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.store.DeleteAPIToken(id); err != nil {
		h.host.RedirectAction(w, r, "/settings/api-tokens?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/settings/api-tokens?toast=token+revoked&toast-type=success")
}

// --- Audit (W10) ---

// AuditPage renders /settings/audit with filters + pagination.
func (h *Handler) AuditPage(w http.ResponseWriter, r *http.Request) {
	if h.auditStore == nil {
		http.Error(w, "audit store unavailable", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	f := audit.Filter{
		User:     strings.TrimSpace(q.Get("user")),
		Resource: strings.TrimSpace(q.Get("resource")),
		Verb:     strings.TrimSpace(q.Get("verb")),
	}
	if raw := q.Get("since"); raw != "" {
		if t, err := time.Parse("2006-01-02T15:04", raw); err == nil {
			f.Since = t
		} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.Since = t
		}
	}
	if raw := q.Get("until"); raw != "" {
		if t, err := time.Parse("2006-01-02T15:04", raw); err == nil {
			f.Until = t
		} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.Until = t
		}
	}
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	f.Limit = limit
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}

	events, err := h.auditStore.ListEvents(r.Context(), f)
	if err != nil {
		http.Error(w, "list audit failed", http.StatusInternalServerError)
		return
	}
	total, err := h.auditStore.CountEvents(r.Context(), f)
	if err != nil {
		http.Error(w, "count audit failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.AuditRow, 0, len(events))
	for _, ev := range events {
		rows = append(rows, pages.AuditRow{
			ID: ev.ID, Timestamp: ev.Timestamp, UserName: ev.UserName, Role: ev.Role,
			Method: ev.Method, Path: ev.Path, Resource: ev.Resource, ResourceID: ev.ResourceID,
			Verb: ev.Verb, Status: ev.Status, RequestID: ev.RequestID, SourceIP: ev.SourceIP,
			Error: ev.Error,
		})
	}

	userSet := map[string]struct{}{}
	resSet := map[string]struct{}{}
	for _, ev := range events {
		if ev.UserName != "" {
			userSet[ev.UserName] = struct{}{}
		}
		if ev.Resource != "" {
			resSet[ev.Resource] = struct{}{}
		}
	}

	data := pages.AuditPageData{
		Events: rows,
		Filters: pages.AuditFilters{
			User: f.User, Resource: f.Resource, Verb: f.Verb,
			Since: q.Get("since"), Until: q.Get("until"),
		},
		Total:     total,
		Limit:     limit,
		Offset:    f.Offset,
		CanMutate: h.host.CanMutate(r),
	}
	for u := range userSet {
		data.Users = append(data.Users, u)
	}
	for r := range resSet {
		data.Resources = append(data.Resources, r)
	}

	h.host.Render(w, r, layout.BaseProps{
		Title:   "Audit Log",
		Page:    "audit",
		Content: pages.AuditPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "Audit", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// --- Providers + manual join (W11) ---

// ProvidersPage renders /providers.
func (h *Handler) ProvidersPage(w http.ResponseWriter, r *http.Request) {
	providers, err := h.store.ListProviders()
	if err != nil {
		http.Error(w, "list providers failed", http.StatusInternalServerError)
		return
	}
	rows := make([]pages.ProviderRow, 0, len(providers))
	for _, p := range providers {
		row := pages.ProviderRow{
			Type:      p.Metadata.Name,
			Endpoint:  p.Spec.Endpoint,
			Connected: p.Status.Connected,
		}
		if !p.Status.LastHeartbeat.IsZero() {
			row.LastHeartbeat = p.Status.LastHeartbeat.Format(time.RFC3339)
		} else {
			row.LastHeartbeat = "—"
		}
		if p.Status.Schema != nil {
			row.MachineTypes = p.Status.Schema.MachineTypes
			row.Regions = p.Status.Schema.Regions
		}
		row.Error = p.Status.Error
		rows = append(rows, row)
	}
	h.host.Render(w, r, layout.BaseProps{
		Title:   "Providers",
		Page:    "providers",
		Content: pages.ProvidersPage(pages.ProvidersPageData{Providers: rows, Total: len(rows), CanMutate: h.host.CanMutate(r)}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Providers", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// ManualJoinPage renders /machines/join-manual.
func (h *Handler) ManualJoinPage(w http.ResponseWriter, r *http.Request) {
	endpoint := os.Getenv("REZUSCLOUD_MACHINELINK_PUBLIC_ENDPOINT")
	if endpoint == "" {
		endpoint = h.host.MachineLinkEndpoint()
	}

	jtRecords, _, err := h.store.ListJoinTokens()
	if err != nil {
		http.Error(w, "list join tokens failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.ManualJoinToken, 0, len(jtRecords))
	for _, jt := range jtRecords {
		if jt.Status.Used {
			continue
		}
		if !jt.Spec.ExpiresAt.IsZero() && time.Now().UTC().After(jt.Spec.ExpiresAt) {
			continue
		}
		tokenPreview := jt.Metadata.Name
		if len(tokenPreview) > 8 {
			tokenPreview = tokenPreview[:8] + "…"
		}
		cluster := jt.Metadata.Labels["rezuscloud.io/tenant"]
		expires := ""
		if !jt.Spec.ExpiresAt.IsZero() {
			expires = jt.Spec.ExpiresAt.Format(time.RFC3339)
		}
		rows = append(rows, pages.ManualJoinToken{
			Token:      tokenPreview,
			Cluster:    cluster,
			NodeGroup:  jt.Spec.NodeGroup,
			KernelArgs: kernelArgsPreview(jt.Metadata.Name, endpoint),
			ExpiresAt:  expires,
		})
	}

	data := pages.ManualJoinPageData{
		ClusterNames: h.host.TenantNames(),
		JoinTokens:   rows,
		CanMutate:    h.host.CanMutate(r),
	}
	if u := os.Getenv("REZUSCLOUD_IMAGE_FACTORY_URL"); u != "" {
		data.HelperURL = u
		data.HelperText = "Generate a Talos installation image that boots with your kernel args."
	} else {
		data.HelperURL = "https://factory.talos.dev/"
		data.HelperText = "Use Image Factory to generate a Talos ISO or raw image; boot it with the kernel args below."
	}

	h.host.Render(w, r, layout.BaseProps{
		Title:   "Manual Join",
		Page:    "machines",
		Content: pages.ManualJoinPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: "Manual Join", Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

// kernelArgsPreview renders the kernel args a machine should boot with.
// Duplicated from the root web.Handler because the machines section (which
// also uses it) is still in the root package; #56 will move the original
// here and remove the duplicate.
func kernelArgsPreview(token, endpoint string) string {
	return fmt.Sprintf(
		"siderolink.api=https://%s?jointoken=%s\ntalos.platform=metal\ntalos.config=.siderolink",
		endpoint, token,
	)
}
