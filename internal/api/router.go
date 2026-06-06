// Package api provides HTTP handlers for the management plane REST API.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/api/jointoken"
	"github.com/rezuscloud/rezuscloud/internal/api/logs"
	"github.com/rezuscloud/rezuscloud/internal/api/machine"
	"github.com/rezuscloud/rezuscloud/internal/api/middleware"
	"github.com/rezuscloud/rezuscloud/internal/api/nodegroup"
	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/api/provider"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/backup"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
)

// Router creates and returns a fully configured HTTP handler for the API.
// auditComponent is required — the API runs audit middleware on every mutation.
// backupComponent may be nil — if nil, /api/v1/backups/* is not registered.
// upgradeManager is required — owns upgrade run lifecycle.
func Router(store *state.Store, jwtManager *auth.JWTManager, auditComponent *audit.Component, backupComponent *backup.Component, upgradeManager *upgrade.Manager) http.Handler {
	mux := http.NewServeMux()

	// Public endpoints (no auth required).
	authHandlers := auth.NewAuthHandlers(store, jwtManager)
	authHandlers.RegisterRoutes(mux)

	// Protected endpoints (auth required).
	protected := http.NewServeMux()

	// Tenant endpoints — view role minimum.
	tenantAPI := NewTenantAPI(store)
	tenantAPI.RegisterRoutes(protected)

	// NodeGroup endpoints — nested under tenants.
	ngAPI := nodegroup.NewAPI(store)
	ngAPI.RegisterRoutes(protected)

	// Machine endpoints (cluster-wide + tenant-scoped).
	machineAPI := machine.NewAPI(store)
	machineAPI.RegisterRoutes(protected)

	// Provider endpoints.
	providerAPI := provider.NewAPI(store)
	providerAPI.RegisterRoutes(protected)

	// JoinToken endpoints — nested under tenants.
	jtAPI := jointoken.NewAPI(store)
	jtAPI.RegisterRoutes(protected)

	// ConfigPatch endpoints — nested under tenants.
	patchAPI := patch.NewAPI(store)
	patchAPI.RegisterRoutes(protected)

	// Machine log streaming.
	logHandler := logs.NewHandler(logs.NewStoreLogProvider(store))
	logHandler.RegisterRoutes(protected)

	// Upgrade endpoints.
	upgradeAPI := upgrade.NewAPI(store, nil, upgradeManager)
	upgradeAPI.RegisterRoutes(protected)

	// Backup endpoints (optional — registered only if component is provided).
	if backupComponent != nil {
		backupComponent.API.RegisterRoutes(protected)
	}

	// User management — admin only.
	userHandlers := auth.NewUserHandlers(store)
	userMux := http.NewServeMux()
	userHandlers.RegisterRoutes(userMux)
	protected.Handle("/api/v1/users/", auth.RequireRole(auth.RoleAdmin)(userMux))
	protected.Handle("/api/v1/users", auth.RequireRole(auth.RoleAdmin)(userMux))

	// API tokens — per-user ownership enforced inside handlers.
	apiTokenHandlers := auth.NewAPITokenHandlers(store)
	apiTokenHandlers.RegisterRoutes(protected)

	// Whoami — any authenticated user.
	whoamiHandlers := auth.NewAuthHandlers(store, jwtManager)
	protected.HandleFunc("GET /api/v1/auth/whoami", whoamiHandlers.Whoami)

	// System endpoints.
	RegisterSystemRoutes(protected, store)

	// Apply auth middleware to protected routes (JWT + API tokens).
	// Audit middleware sits after auth so it can resolve user/role from context.
	auditHandlers := auditComponent.Handlers
	auditHandlers.RegisterRoutes(protected)

	protectedWithAudit := audit.Middleware(auditComponent.Recorder)(protected)
	mux.Handle("/api/v1/", auth.AuthenticateWithTokens(jwtManager, auth.StoreTokenVerifier{Store: store}, protectedWithAudit))

	return middleware.Chain(mux,
		middleware.Recovery,
		middleware.Logging,
	)
}

// RegisterSystemRoutes registers system status routes.
func RegisterSystemRoutes(mux *http.ServeMux, _ *state.Store) {
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":      "ok",
			"machinelink": "listening",
			"provider":    "listening",
		})
	})
}
