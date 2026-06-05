package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/ingress"
	"github.com/rezuscloud/rezuscloud/internal/state"
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

	// Start MachineLink server.
	linkListener, err := net.Listen("tcp", cfg.MachineLinkAddr)
	if err != nil {
		log.Fatalf("machinelink listen: %v", err)
	}
	log.Printf("  machinelink: %s", linkListener.Addr())

	go serveMachineLink(ctx, linkListener, store)

	// Start Provider gRPC server.
	providerListener, err := net.Listen("tcp", cfg.ProviderAddr)
	if err != nil {
		log.Fatalf("provider listen: %v", err)
	}
	log.Printf("  provider gRPC: %s", providerListener.Addr())

	go serveProviderGRPC(ctx, providerListener, store)

	// Start HTTP server (WebUI + health + API).
	mux := http.NewServeMux()
	registerHealthHandlers(mux)
	registerVersionHandler(mux)

	// API router with middleware (recovery, logging, auth).
	// Registered with explicit methods because the WebUI registers method-scoped
	// routes ("GET /", "GET /tenants", ...) and Go 1.22+ ServeMux panics when
	// method-scoped and method-less patterns share a path prefix.
	apiRouter := api.Router(store, jwtManager)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		mux.Handle(method+" /api/", apiRouter)
	}

	// WebUI.
	webHandler := web.NewHandler(store, jwtManager, bus)
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
	_ = linkListener.Close()      //nolint:errcheck // listener close on shutdown
	_ = providerListener.Close()  //nolint:errcheck // listener close on shutdown
	_ = store.Close()             //nolint:errcheck // store close on shutdown
	log.Println("stopped")
}

// config holds the management plane configuration.
type config struct {
	Addr            string // HTTP listen address
	DataDir         string // Persistent data directory
	Mode            string // "standalone" or "cluster"
	MachineLinkAddr string // MachineLink gRPC listen address
	ProviderAddr    string // Provider gRPC listen address
	JoinToken       string // Global join token for machine authentication
	JWTSecret       string // JWT signing secret
	AdminPassword   string // Initial admin password
}

func loadConfig() config {
	return config{
		Addr:            envOr("REZUSCLOUD_ADDR", ":8080"),
		DataDir:         envOr("REZUSCLOUD_DATA_DIR", "/data"),
		Mode:            envOr("REZUSCLOUD_MODE", "standalone"),
		MachineLinkAddr: envOr("REZUSCLOUD_MACHINELINK_ADDR", ":50180"),
		ProviderAddr:    envOr("REZUSCLOUD_PROVIDER_ADDR", ":50190"),
		JoinToken:       os.Getenv("REZUSCLOUD_JOIN_TOKEN"),
		JWTSecret:       envOr("REZUSCLOUD_JWT_SECRET", ""),
		AdminPassword:   os.Getenv("REZUSCLOUD_ADMIN_PASSWORD"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- MachineLink Server (stub → real implementation later) ---

func serveMachineLink(ctx context.Context, ln net.Listener, _ *state.Store) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("machinelink accept: %v", err)
				return
			}
		}
		go handleMachineLinkConn(ctx, conn)
	}
}

func handleMachineLinkConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	log.Printf("machinelink: connection from %s", conn.RemoteAddr())
	<-ctx.Done()
}

// --- Provider gRPC Server (stub → real implementation later) ---

func serveProviderGRPC(ctx context.Context, ln net.Listener, _ *state.Store) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("provider accept: %v", err)
				return
			}
		}
		go handleProviderConn(ctx, conn)
	}
}

func handleProviderConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	log.Printf("provider: connection from %s", conn.RemoteAddr())
	<-ctx.Done()
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
