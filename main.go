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
	"strings"
	"syscall"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api"
	"github.com/rezuscloud/rezuscloud/internal/applyqueue"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/backup"
	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/ingress"
	"github.com/rezuscloud/rezuscloud/internal/metrics"
	"github.com/rezuscloud/rezuscloud/internal/projection"
	"github.com/rezuscloud/rezuscloud/internal/provider"
	providermetal "github.com/rezuscloud/rezuscloud/internal/provider/metal"
	provideroci "github.com/rezuscloud/rezuscloud/internal/provider/oci"
	provideros "github.com/rezuscloud/rezuscloud/internal/provider/openstack"
	"github.com/rezuscloud/rezuscloud/internal/reconcile"
	"github.com/rezuscloud/rezuscloud/internal/state"
	statuspkg "github.com/rezuscloud/rezuscloud/internal/status"
	"github.com/rezuscloud/rezuscloud/internal/tfbackend"
	"github.com/rezuscloud/rezuscloud/internal/tfexec"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
	talosupgrade "github.com/rezuscloud/rezuscloud/internal/upgrade/talos"
	"github.com/rezuscloud/rezuscloud/internal/watch"
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

	// TF HTTP backend: RezusCloud is the remote state store tofu writes one
	// encrypted state blob per tenant to (ADR 21). Shares the management DB.
	tfStore, err := tfbackend.New(store.DB())
	if err != nil {
		log.Fatalf("tfbackend init: %v", err)
	}

	// Initialize watch bus (NATS embedded, per ADR 0009) + wire into store mutations.
	natsBus, err := watch.NewNATSBus()
	if err != nil {
		log.Fatalf("nats bus init: %v", err)
	}
	defer natsBus.Close()
	var bus watch.Bus = natsBus

	// Provider registry: maps ProviderClass prefixes to renderer modules.
	registry := provider.NewRegistry()
	registry.Register(provideroci.New())
	registry.Register(provideros.New())
	registry.Register(providermetal.New())

	// TF execution engine: runs tofu in per-tenant workdirs, reading/writing
	// state through RezusCloud's own HTTP backend (started below).
	backendURL := "http://127.0.0.1:" + portFromAddr(cfg.Addr) + "/tfstate"
	// Encryption: only enable when the passphrase is set. Without it, state
	// is stored unencrypted (fine for dev/test; production must set the env var).
	var tfOpts []tfexec.Option
	tfOpts = append(tfOpts, tfexec.WithBackendURL(backendURL))
	if cfg.StatePassphrase != "" {
		tfOpts = append(tfOpts, tfexec.WithEncryption(cfg.StatePassphrase))
		log.Printf("  state encryption: enabled")
	} else {
		log.Printf("  state encryption: disabled (set REZUSCLOUD_STATE_PASSPHRASE to enable)")
	}
	tfExec, err := tfexec.New(filepath.Join(cfg.DataDir, "tfwork"), tfOpts...)
	if err != nil {
		log.Fatalf("tfexec init: %v", err)
	}

	// Secrets cache: in-memory tenant credentials, shared across subsystems
	// (status-plane probes + the upgrade adapter). Refreshed after each apply.
	secretsCache := credentials.NewSecretsCache(credentials.StoreSource(store))

	// Upgrade engine: owns the rolling upgrade loop + run persistence. Injected
	// as the pre-apply upgrade hook (#93) so machines converge before tofu apply.
	machineUpgrader := talosupgrade.New(secretsCache, store)
	upgradeMgr := upgrade.NewManager(store, machineUpgrader, upgrade.NewStoreMachineLister(store))

	// Apply queue: debounced per-tenant reconciliation scheduler (#87a). Driven
	// by the production Applier (#87b/#99) which renders .tf.json + runs tofu.
	applier := reconcile.NewApplier(tfExec, registry, store, reconcile.WithUpgradeRunner(upgradeMgr))

	// Projection index: TF state → K8s-style resource read model (#91). Rebuilt
	// after each successful apply by the queue's listener.
	projIndex := projection.New(
		projection.StateSourceFunc(tfExec.StatePull),
		registry,
	)
	projIndex.RegisterExtractor("Machine", machineExtractor)

	tenantLister := func() ([]string, error) {
		tenants, _, err := store.ListTenants()
		if err != nil {
			return nil, err
		}
		names := make([]string, len(tenants))
		for i, t := range tenants {
			names[i] = t.Metadata.Name
		}
		return names, nil
	}
	statusTracker := reconcile.NewStatusTracker(store)
	statusTracker.Start(ctx)
	defer statusTracker.Stop()

	// Secrets cache listener: refreshed after PhaseApplied.
	// (secretsCache was created earlier, shared with the upgrade adapter.)

	statusListener := statusTracker.Listener()
	projectionListener := reconcile.ProjectionListener(projIndex)
	storeEnricher := reconcile.NewStoreEnricher(store, projIndex)
	enrichListener := storeEnricher.Listener()
	secretsListener := func(tenant string, phase applyqueue.Phase, err error) {
		if phase == applyqueue.PhaseApplied {
			secretsCache.Refresh(ctx, tenant)
		}
	}
	queue := applyqueue.New(applier, tenantLister,
		func(tenant string, phase applyqueue.Phase, err error) {
			statusListener(tenant, phase, err)
			projectionListener(tenant, phase, err)
			enrichListener(tenant, phase, err)
			secretsListener(tenant, phase, err)
		},
		applyqueue.Config{},
	)
	queue.Start(ctx)
	defer queue.Stop()

	// MultiBus: fan out store mutations to BOTH the watch SSE adapter AND the
	// reconcile enqueue bus. A tenant/nodegroup mutation triggers a debounced
	// apply via the queue.
	store.SetBus(state.NewMultiBus(
		watch.NewAdapter(bus),
		reconcile.NewEnqueueBus(queue),
	))

	// Initialize auth.
	jwtManager := auth.NewJWTManager(cfg.JWTSecret)
	ensureAdminUser(store, cfg)

	// Start HTTP server (WebUI + health + API).
	mux := http.NewServeMux()
	registerHealthHandlers(mux)
	registerVersionHandler(mux)

	// TF HTTP backend routes (tofu's remote state endpoint).
	tfbackend.NewHandler(tfStore).RegisterRoutes(mux, "/tfstate")

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

	// Upgrade subsystem: one Manager owns run lifecycle (moved up for the
	// pre-apply hook; keep the API wiring here).
	// upgradeMgr already constructed above.

	// Status gatherer (ADR 0016): on-demand tenant health probe with short TTL.
	statusGatherer := statuspkg.NewGatherer(store, secretsCache, nil)

	// API router with middleware (recovery, logging, auth).
	// Registered with explicit methods because the WebUI registers method-scoped
	// routes ("GET /", "GET /tenants", ...) and Go 1.22+ ServeMux panics when
	// method-scoped and method-less patterns share a path prefix.
	apiRouter := api.Router(store, jwtManager, auditComponent, backupComponent, upgradeMgr, projIndex, statusGatherer)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		mux.Handle(method+" /api/", apiRouter)
	}

	// WebUI.
	webHandler := web.NewHandler(store, jwtManager, bus).
		WithAuditComponent(auditComponent).
		WithBackupComponent(backupComponent).
		WithUpgradeManager(upgradeMgr).
		WithMachineActions(machineUpgrader)

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
	Addr            string // HTTP listen address
	DataDir         string // Persistent data directory
	Mode            string // "standalone" or "cluster"
	JWTSecret       string // JWT signing secret
	AdminPassword   string // Initial admin password
	PrometheusURL   string // Prometheus query endpoint (e.g. http://prometheus:9090)
	K8sAPIURL       string // Kubernetes API server URL (e.g. https://kubernetes.default.svc)
	StatePassphrase string // OpenTofu state encryption passphrase (ADR 0005)
}

func loadConfig() config {
	return config{
		Addr:            envOr("REZUSCLOUD_ADDR", ":8080"),
		DataDir:         envOr("REZUSCLOUD_DATA_DIR", "/data"),
		Mode:            envOr("REZUSCLOUD_MODE", "standalone"),
		JWTSecret:       envOr("REZUSCLOUD_JWT_SECRET", ""),
		AdminPassword:   os.Getenv("REZUSCLOUD_ADMIN_PASSWORD"),
		PrometheusURL:   os.Getenv("REZUSCLOUD_PROMETHEUS_URL"),
		K8sAPIURL:       os.Getenv("REZUSCLOUD_K8S_API_URL"),
		StatePassphrase: os.Getenv("REZUSCLOUD_STATE_PASSPHRASE"),
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

// portFromAddr extracts the port from a ":PORT" or "HOST:PORT" listen address.
// Falls back to "8080" if parsing fails.
func portFromAddr(addr string) string {
	// Handle ":PORT" (most common) and "HOST:PORT".
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i+1:]
	}
	return "8080"
}

// machineExtractor pulls Machine-relevant fields from a TF instance's
// attributes for the projection index. Works across providers that expose
// standard instance attributes (id, public_ip, private_ip, shape, display_name).
func machineExtractor(tfType string, attrs map[string]interface{}) map[string]interface{} {
	if attrs == nil {
		return nil
	}
	spec := map[string]interface{}{}
	for _, k := range []string{"id", "public_ip", "private_ip", "shape", "display_name", "state"} {
		if v, ok := attrs[k]; ok {
			spec[k] = v
		}
	}
	if len(spec) == 0 {
		return nil
	}
	return spec
}
