package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/backup"
	"github.com/rezuscloud/rezuscloud/internal/ingress"
	"github.com/rezuscloud/rezuscloud/internal/metrics"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/watchbus"
	"github.com/rezuscloud/rezuscloud/internal/web"
	"github.com/rezuscloud/rezuscloud/version"
)

func main() {
	cfg := loadConfig()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("rezuscloud management plane starting")
	log.Printf("  version: %s", version.Version)
	log.Printf("  commit:  %s", version.GitCommit)
	log.Printf("  built:   %s", version.BuildTime)
	log.Printf("  addr: %s", cfg.Addr)
	log.Printf("  data dir: %s", cfg.DataDir)
	log.Printf("  mode: %s", cfg.Mode)

	// Initialize state store.
	store, err := state.Open(filepath.Join(cfg.DataDir, "rezuscloud.db"))
	if err != nil {
		log.Fatalf("state init: %v", err)
	}

	// Initialize watch bus + wire into store mutations.
	bus := watch.NewBus()
	store.SetBus(watchbus.New(bus))

	// Initialize auth.
	jwtManager := auth.NewJWTManager(cfg.JWTSecret)
	ensureAdminUser(store, cfg)

	// Start HTTP server (WebUI + health + API).
	mux := http.NewServeMux()
	registerHealthHandlers(mux)
	registerVersionHandler(mux)

	// Audit subsystem: one component owns Store + Recorder + Handlers + Retention.
	// Passed to both the API router and the WebUI handler.
	retentionDays := atoiDefault(os.Getenv("REZUSCLOUD_AUDIT_RETENTION_DAYS"), 90)
	auditComponent := audit.NewComponent(store.DB(), audit.ComponentOptions{RetentionDays: retentionDays})
	go auditComponent.StartRetention(ctx)
	defer auditComponent.Close()

	// Backup subsystem: one component owns Manager + Service + API.
	backupDir := os.Getenv("REZUSCLOUD_BACKUP_DIR")
	backupComponent, backupErr := backup.NewComponent(store, backup.ComponentOptions{Root: backupDir})
	if backupErr != nil {
		log.Printf("backup subsystem disabled: %v", backupErr)
	}

	// Upgrade subsystem: one Manager owns run lifecycle.
	upgradeMgr := upgrade.NewManager(store)

	// API router with middleware (recovery, logging, auth).
	// Registered with explicit methods because the WebUI registers method-scoped
	// routes ("GET /", "GET /tenants", ...) and Go 1.22+ ServeMux panics when
	// method-scoped and method-less patterns share a path prefix.
	apiRouter := api.Router(store, jwtManager, auditComponent, backupComponent, upgradeMgr)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		mux.Handle(method+" /api/", apiRouter)
	}

	// WebUI.
	webHandler := web.NewHandler(store, jwtManager, bus).
		WithAuditComponent(auditComponent).
		WithBackupComponent(backupComponent).
		WithUpgradeManager(upgradeMgr)

	// Resource pressure visualization (optional — requires Prometheus + K8s API access).
	if cfg.PrometheusURL != "" && cfg.K8sAPIURL != "" {
		agg := &metrics.Aggregator{
			Prom: &metrics.PrometheusClient{BaseURL: cfg.PrometheusURL},
			K8s:  &metrics.K8sMetricsClient{BaseURL: cfg.K8sAPIURL},
		}
		webHandler.WithMetricsAggregator(agg)
	}

	webHandler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           ingress.Middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("  http: %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("shutting down...")
	case err := <-errCh:
		if err != nil {
			log.Fatalf("error: %v", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Best-effort shutdown — ignore errors (context already cancelled, connections closing).
	_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // shutdown in deferred context
	_ = store.Close()             //nolint:errcheck // store close on shutdown
	log.Println("stopped")
}

// config holds the management plane configuration.
type config struct {
	Addr          string // HTTP listen address
	DataDir       string // Persistent data directory
	Mode          string // "standalone" or "cluster"
	JoinToken     string // Global join token for machine authentication
	JWTSecret     string // JWT signing secret
	AdminPassword string // Initial admin password
	PrometheusURL string // Prometheus query endpoint (e.g. http://prometheus:9090)
	K8sAPIURL     string // Kubernetes API server URL (e.g. https://kubernetes.default.svc)
}

func loadConfig() config {
	return config{
		Addr:          envOr("REZUSCLOUD_ADDR", ":8080"),
		DataDir:       envOr("REZUSCLOUD_DATA_DIR", "/data"),
		Mode:          envOr("REZUSCLOUD_MODE", "standalone"),
		JoinToken:     os.Getenv("REZUSCLOUD_JOIN_TOKEN"),
		JWTSecret:     envOr("REZUSCLOUD_JWT_SECRET", ""),
		AdminPassword: os.Getenv("REZUSCLOUD_ADMIN_PASSWORD"),
		PrometheusURL: os.Getenv("REZUSCLOUD_PROMETHEUS_URL"),
		K8sAPIURL:     os.Getenv("REZUSCLOUD_K8S_API_URL"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- HTTP Handlers ---

func registerHealthHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ready")
	})
}

func registerVersionHandler(mux *http.ServeMux) {
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(version.Get())
	})
}

// ensureAdminUser creates the initial admin user if no users exist.
// The JWT secret is auto-generated if not provided, and used as the basis for the admin password.
func ensureAdminUser(store *state.Store, cfg config) {
	users, err := store.ListUsers()
	if err != nil {
		log.Printf("warning: could not check existing users: %v", err)
		return
	}
	if len(users) > 0 {
		return
	}

	password := cfg.AdminPassword
	if password == "" {
		log.Println("no admin password set via REZUSCLOUD_ADMIN_PASSWORD")
		log.Println("create user with: curl -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{\"metadata\":{\"name\":\"admin\"},\"spec\":{\"role\":\"admin\",\"password\":\"YOUR_PASSWORD\"}}'")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Printf("warning: could not hash admin password: %v", err)
		return
	}

	_, err = store.CreateUser("admin", state.UserSpec{
		Role:         auth.RoleAdmin,
		PasswordHash: hash,
	})
	if err != nil {
		log.Printf("warning: could not create admin user: %v", err)
		return
	}

	log.Println("created initial admin user (username: admin)")
}

// atoiDefault parses s as an integer; returns fallback on parse error or when
// s is empty.
func atoiDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}
