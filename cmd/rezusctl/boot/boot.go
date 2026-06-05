// Package boot implements the 'rezusctl boot' command.
package boot

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	dockerboot "github.com/rezuscloud/rezuscloud/internal/cli/boot"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform/docker"
	"github.com/rezuscloud/rezuscloud/internal/cli/state"
)

// Options holds the configuration for the boot command.
type Options struct {
	ClusterName   string
	Platform      string
	Management    bool
	ControlPlanes int
	Workers       int
	TalosVersion  string
	CiliumVersion string
	StateDir      string
	Out           io.Writer
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() *Options {
	return &Options{
		ClusterName:   "rezuscloud",
		Platform:      "docker",
		ControlPlanes: 1,
		Workers:       0,
		TalosVersion:  "latest",
		CiliumVersion: "1.19.3",
		StateDir:      ".rezusctl",
		Out:           os.Stdout,
	}
}

// Complete fills in any missing options from defaults and environment variables.
// Environment variables are checked after explicit flags, allowing HA add-ons
// and containers to configure rezusctl without command-line arguments.
func (o *Options) Complete() error {
	// Environment variable fallbacks
	if o.ClusterName == "" {
		o.ClusterName = envOr("REZUSCTL_CLUSTER_NAME", "rezuscloud")
	}
	if o.StateDir == "" {
		o.StateDir = envOr("REZUSCTL_STATE_DIR", ".rezusctl")
	}
	if o.TalosVersion == "" || o.TalosVersion == "latest" {
		if v := os.Getenv("REZUSCTL_TALOS_VERSION"); v != "" {
			o.TalosVersion = v
		}
	}
	if o.CiliumVersion == "" {
		if v := os.Getenv("REZUSCTL_CILIUM_VERSION"); v != "" {
			o.CiliumVersion = v
		}
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	return nil
}

// envOr returns the environment variable value or the fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Validate checks the options for errors.
func (o *Options) Validate() error {
	if o.Platform != "docker" && o.Platform != "qemu" {
		return fmt.Errorf("unsupported platform: %s (supported: docker, qemu)", o.Platform)
	}
	if o.ControlPlanes < 1 {
		return fmt.Errorf("control plane count must be >= 1")
	}
	if o.ControlPlanes > 1 && o.Platform == "docker" {
		return fmt.Errorf("docker platform supports only 1 control plane node")
	}
	return nil
}

// Run executes the boot command.
func (o *Options) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fprintf(o.Out, "rezusctl boot — %s platform\n\n", o.Platform)

	switch o.Platform {
	case "docker":
		return o.bootDocker(ctx)
	default:
		return fmt.Errorf("unsupported platform: %s", o.Platform)
	}
}

func (o *Options) bootDocker(ctx context.Context) error {
	// Docker platform (authenticates with local Docker daemon)
	dp, err := docker.New(ctx)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}

	// State stores
	bootStore := state.NewBootStore(o.StateDir)

	// Boot spec
	spec := dockerboot.DockerBootSpec{
		ClusterName:       o.ClusterName,
		KubernetesVersion: "1.35.0",
		CiliumVersion:     o.CiliumVersion,
		ControlPlanes:     o.ControlPlanes,
		Workers:           o.Workers,
	}

	// Orchestrator
	orch := dockerboot.NewDockerBootOrchestrator(dp, bootStore, o.StateDir, spec, o.Out)

	// Event channel for progress updates
	events := make(chan dockerboot.Event, 100)
	go func() {
		for range events {
			// Events are logged by the orchestrator directly
		}
	}()

	if err := orch.Run(ctx, events); err != nil {
		return fmt.Errorf("boot: %w", err)
	}

	close(events)

	fprintf(o.Out, "\nCluster %s ready!\n", o.ClusterName)
	fprintf(o.Out, "Kubeconfig: %s\n", dp.KubeconfigEndpoint())
	fprintf(o.Out, "State: %s\n", o.StateDir)

	return nil
}

func fprintf(w io.Writer, format string, args ...interface{}) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}
