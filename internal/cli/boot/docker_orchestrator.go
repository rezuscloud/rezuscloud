package boot

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/cli/helm"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform/docker"
	"github.com/rezuscloud/rezuscloud/internal/cli/provider"
	"github.com/rezuscloud/rezuscloud/internal/cli/provider/cilium"
	"github.com/rezuscloud/rezuscloud/internal/cli/state"
	"github.com/rezuscloud/rezuscloud/internal/cli/talosconfig"
)

// DockerBootSpec defines the desired Docker cluster configuration.
type DockerBootSpec struct {
	ClusterName       string `json:"clusterName"`
	KubernetesVersion string `json:"kubernetesVersion"`
	CiliumVersion     string `json:"ciliumVersion"`
	ControlPlanes     int    `json:"controlPlanes"`
	Workers           int    `json:"workers"`
}

// DockerBootOrchestrator drives the full Docker boot sequence.
type DockerBootOrchestrator struct {
	docker    *docker.DockerPlatform
	bootStore *state.BootStore
	stateDir  string
	spec      DockerBootSpec
	generator *talosconfig.Generator
	out       io.Writer
	steps     []Step
}

// NewDockerBootOrchestrator creates a boot orchestrator for Docker clusters.
func NewDockerBootOrchestrator(dp *docker.DockerPlatform, bootStore *state.BootStore, stateDir string, spec DockerBootSpec, out io.Writer) *DockerBootOrchestrator {
	return &DockerBootOrchestrator{
		docker:    dp,
		bootStore: bootStore,
		stateDir:  stateDir,
		spec:      spec,
		out:       out,
		steps: []Step{
			{Name: "auth", Description: "Validate Docker access"},
			{Name: "provision", Description: "Create Docker network"},
			{Name: "generate-config", Description: "Generate Talos machine configs"},
			{Name: "create-nodes", Description: "Create and start Talos containers"},
			{Name: "bootstrap", Description: "Wait for Kubernetes API"},
			{Name: "install-cni", Description: "Configure pod networking"},
			{Name: "verify", Description: "Verify cluster health"},
		},
	}
}

// RunPartial executes steps up to and including the named target step.
// Useful for testing individual phases without waiting for full bootstrap.
func (o *DockerBootOrchestrator) RunPartial(ctx context.Context, events chan<- Event, targetStep string) error {
	targetIndex := -1
	for i, s := range o.steps {
		if s.Name == targetStep {
			targetIndex = i
			break
		}
	}
	if targetIndex == -1 {
		return fmt.Errorf("unknown step: %s", targetStep)
	}

	completed, err := o.loadCompletedSteps(ctx)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	for i := 0; i <= targetIndex; i++ {
		step := o.steps[i]

		if completed[step.Name] {
			o.steps[i].Status = StatusComplete
			o.emit(events, "step-skipped", step.Name, "already completed")
			fprintf(o.out, "[skip] %s — already completed\n", step.Name)
			continue
		}

		o.markRunning(i)
		o.emit(events, "step-start", step.Name, step.Description)
		fprintf(o.out, "[%d/%d] %s — %s\n", i+1, targetIndex+1, step.Name, step.Description)

		stepStart := time.Now()
		err := o.executeStep(ctx, i, events)
		duration := time.Since(stepStart)

		if err != nil {
			o.markFailed(i, err)
			o.emit(events, "step-failed", step.Name, err.Error())
			fprintf(o.out, "[fail] %s: %v (%s)\n", step.Name, err, duration.Truncate(time.Millisecond))
			return fmt.Errorf("step %q: %w", step.Name, err)
		}

		o.markComplete(i, duration)
		o.recordStepComplete(ctx, step.Name)
		o.emit(events, "step-complete", step.Name, fmt.Sprintf("done in %s", duration.Truncate(time.Millisecond)))
		fprintf(o.out, "[ok]   %s (%s)\n", step.Name, duration.Truncate(time.Millisecond))
	}

	return nil
}

// Run executes the full boot sequence, skipping already-completed steps.
// If boot.json says complete but Docker containers are missing (e.g. after HA
// reboot, Docker restart), the state is reset and the cluster is re-provisioned.
func (o *DockerBootOrchestrator) Run(ctx context.Context, events chan<- Event) error {
	start := time.Now()

	if err := o.detectDrift(ctx, events); err != nil {
		return fmt.Errorf("drift detection: %w", err)
	}

	completed, err := o.loadCompletedSteps(ctx)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	for i, step := range o.steps {
		if completed[step.Name] {
			o.steps[i].Status = StatusComplete
			o.emit(events, "step-skipped", step.Name, "already completed")
			fprintf(o.out, "[skip] %s — already completed\n", step.Name)
			continue
		}

		o.markRunning(i)
		o.emit(events, "step-start", step.Name, step.Description)
		fprintf(o.out, "[%d/%d] %s — %s\n", i+1, len(o.steps), step.Name, step.Description)

		stepStart := time.Now()
		err := o.executeStep(ctx, i, events)
		duration := time.Since(stepStart)

		if err != nil {
			o.markFailed(i, err)
			o.emit(events, "step-failed", step.Name, err.Error())
			fprintf(o.out, "[fail] %s: %v (%s)\n", step.Name, err, duration.Truncate(time.Millisecond))
			return fmt.Errorf("step %q: %w", step.Name, err)
		}

		o.markComplete(i, duration)
		o.recordStepComplete(ctx, step.Name)
		o.emit(events, "step-complete", step.Name, fmt.Sprintf("done in %s", duration.Truncate(time.Millisecond)))
		fprintf(o.out, "[ok]   %s (%s)\n", step.Name, duration.Truncate(time.Millisecond))
	}

	fprintf(o.out, "\nCluster %s ready in %s\n", o.spec.ClusterName, time.Since(start).Truncate(time.Millisecond))
	o.emit(events, "boot-complete", "", fmt.Sprintf("cluster %s ready", o.spec.ClusterName))
	return nil
}

// Status returns the current boot progress.
func (o *DockerBootOrchestrator) Status(_ context.Context) (*Progress, error) {
	currentStep := 0
	for i, s := range o.steps {
		if s.Status == StatusComplete {
			currentStep = i + 1
		}
	}
	return &Progress{
		ClusterName: o.spec.ClusterName,
		Platform:    "docker",
		TotalSteps:  len(o.steps),
		CurrentStep: currentStep,
		Steps:       o.steps,
		Complete:    currentStep == len(o.steps),
	}, nil
}

// Destroy tears down all provisioned resources.
func (o *DockerBootOrchestrator) Destroy(ctx context.Context) error {
	fprintf(o.out, "Destroying cluster %s...\n", o.spec.ClusterName)
	err := o.docker.Destroy(ctx, &platform.ClusterSpec{Name: o.spec.ClusterName})
	if err != nil {
		return fmt.Errorf("destroy: %w", err)
	}
	fprintf(o.out, "Cluster %s destroyed\n", o.spec.ClusterName)
	return nil
}

func (o *DockerBootOrchestrator) executeStep(ctx context.Context, stepIndex int, events chan<- Event) error {
	switch o.steps[stepIndex].Name {
	case "auth":
		return o.stepAuth(ctx)
	case "provision":
		return o.stepProvision(ctx)
	case "generate-config":
		return o.stepGenerateConfig(ctx)
	case "create-nodes":
		return o.stepCreateNodes(ctx, events)
	case "bootstrap":
		return o.stepBootstrap(ctx, events)
	case "install-cni":
		return o.stepInstallCNI(ctx)
	case "verify":
		return o.stepVerify(ctx)
	default:
		return fmt.Errorf("unknown step: %s", o.steps[stepIndex].Name)
	}
}

func (o *DockerBootOrchestrator) stepAuth(ctx context.Context) error {
	return o.docker.Auth(ctx, platform.AuthConfig{Provider: "docker"})
}

func (o *DockerBootOrchestrator) stepProvision(ctx context.Context) error {
	_, err := o.docker.Provision(ctx, &platform.ClusterSpec{
		Name:         o.spec.ClusterName,
		TalosVersion: "latest",
	})
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	return nil
}

func (o *DockerBootOrchestrator) stepGenerateConfig(ctx context.Context) error {
	// Use the Docker gateway IP for in-cluster connectivity.
	gatewayEndpoint := talosconfig.InClusterEndpoint(o.docker.GatewayIP(), docker.DefaultKubernetesPort)
	gen, err := talosconfig.NewGenerator(talosconfig.ClusterParams{
		ClusterName:          o.spec.ClusterName,
		ControlPlaneEndpoint: gatewayEndpoint,
		KubernetesVersion:    o.spec.KubernetesVersion,
	})
	if err != nil {
		return fmt.Errorf("create config generator: %w", err)
	}
	o.generator = gen
	o.docker.SetGenerator(gen)

	// Save talosconfig and kubeconfig for the user.
	// Use the host-side endpoint so kubectl works from the host.
	hostEndpoint := o.docker.KubeconfigEndpoint()
	clusterDir := filepath.Join(o.stateDir, o.spec.ClusterName)
	_ = os.MkdirAll(clusterDir, 0o755)

	tc, err := gen.GenerateTalosconfig([]string{hostEndpoint})
	if err == nil {
		_ = os.WriteFile(filepath.Join(clusterDir, "talosconfig"), tc, 0o600)
	}

	kc, err := gen.GenerateKubeconfig([]string{hostEndpoint})
	if err == nil {
		_ = os.WriteFile(filepath.Join(clusterDir, "kubeconfig"), kc, 0o600)
	}

	return nil
}

func (o *DockerBootOrchestrator) stepCreateNodes(ctx context.Context, events chan<- Event) error {
	infra := &platform.Infrastructure{
		ControlPlaneEndpoint: o.docker.KubeconfigEndpoint(),
	}

	total := o.spec.ControlPlanes + o.spec.Workers
	created := 0

	for i := range o.spec.ControlPlanes {
		name := fmt.Sprintf("%s-controlplane-%d", o.spec.ClusterName, i)
		o.emit(events, "create-node", name, fmt.Sprintf("node %d/%d", created+1, total))

		role := "controlplane"
		if i == 0 {
			role = "init"
		}
		nodeSpec := platform.NodeSpec{Name: name, Role: role}
		cfg, err := o.docker.GenerateMachineConfig(nodeSpec, infra)
		if err != nil {
			return fmt.Errorf("config for %s: %w", name, err)
		}

		_, err = o.docker.CreateControlPlane(ctx, o.spec.ClusterName, nodeSpec, cfg)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		created++
	}

	for i := range o.spec.Workers {
		name := fmt.Sprintf("%s-worker-%d", o.spec.ClusterName, i)
		o.emit(events, "create-node", name, fmt.Sprintf("node %d/%d", created+1, total))

		nodeSpec := platform.NodeSpec{Name: name, Role: "worker"}
		cfg, err := o.docker.GenerateMachineConfig(nodeSpec, infra)
		if err != nil {
			return fmt.Errorf("config for %s: %w", name, err)
		}

		_, err = o.docker.CreateWorker(ctx, o.spec.ClusterName, nodeSpec, cfg)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		created++
	}

	return nil
}

func (o *DockerBootOrchestrator) stepBootstrap(ctx context.Context, events chan<- Event) error {
	endpoint := o.docker.KubeconfigEndpoint()
	o.emit(events, "bootstrap", "waiting", fmt.Sprintf("waiting for K8s API at %s", endpoint))
	fprintf(o.out, "  Waiting for Kubernetes API at %s...\n", endpoint)
	return waitForHTTP(ctx, endpoint, 5*time.Minute)
}

func (o *DockerBootOrchestrator) stepInstallCNI(ctx context.Context) error {
	fprintf(o.out, "  Installing Cilium CNI via Helm...\n")

	kubeconfig, err := o.docker.Kubeconfig()
	if err != nil {
		return fmt.Errorf("get kubeconfig: %w", err)
	}

	installer, err := helm.NewInstallerFromBytes(kubeconfig)
	if err != nil {
		return fmt.Errorf("create helm installer: %w", err)
	}

	cniProvider := cilium.NewWithInstaller(installer)

	// Docker-specific Cilium values: no encryption, no IPv6, no host firewall,
	// MTU 1500, kube-proxy replacement. Docker containers run privileged with
	// all capabilities, so eBPF and netadmin work.
	gatewayIP := o.docker.GatewayIP()
	spec := provider.CNISpec{
		Type:          "cilium",
		Version:       o.spec.CiliumVersion,
		MTU:           1500,
		APIServerHost: gatewayIP,
		APIServerPort: docker.DefaultKubernetesPort,
	}

	if err := cniProvider.Install(ctx, nil, spec); err != nil {
		return fmt.Errorf("install cilium: %w", err)
	}

	fprintf(o.out, "  Cilium CNI installed\n")
	return nil
}

func (o *DockerBootOrchestrator) stepVerify(ctx context.Context) error {
	// Verify the K8s API is still responding after Cilium install.
	return waitForHTTP(ctx, o.docker.KubeconfigEndpoint(), 30*time.Second)
}

func (o *DockerBootOrchestrator) loadCompletedSteps(ctx context.Context) (map[string]bool, error) {
	if o.bootStore != nil {
		return o.bootStore.CompletedSteps(ctx, o.spec.ClusterName)
	}
	return make(map[string]bool), nil
}

// detectDrift checks if the cluster infrastructure matches boot.json state.
// If boot.json says complete but Docker containers are missing, the state
// is reset so the cluster is re-provisioned from scratch.
func (o *DockerBootOrchestrator) detectDrift(ctx context.Context, events chan<- Event) error {
	if o.bootStore == nil {
		return nil
	}

	completed, err := o.bootStore.CompletedSteps(ctx, o.spec.ClusterName)
	if err != nil {
		return err
	}

	// Only check drift when boot.json says all steps are done.
	if len(completed) < len(o.steps) {
		return nil
	}

	// Check if Docker containers for the cluster still exist.
	containers, err := docker.FindContainersByCluster(ctx, o.docker.Client(), o.spec.ClusterName)
	if err != nil {
		return fmt.Errorf("check containers: %w", err)
	}

	if len(containers) > 0 {
		// Infrastructure exists, state is consistent.
		return nil
	}

	// Drift detected: state says complete but containers are gone.
	o.emit(events, "drift-detected", "", "boot.json says complete but containers missing, resetting")
	fprintf(o.out, "[drift] boot.json says complete but no containers found — re-provisioning\n")

	// Also clean up the network if it exists.
	_ = o.docker.DestroyNetwork(ctx, o.spec.ClusterName)

	// Reset all steps in boot store.
	for _, step := range o.steps {
		_ = o.bootStore.MarkStep(ctx, o.spec.ClusterName, "docker", step.Name, state.StatusCreated)
	}

	return nil
}

func (o *DockerBootOrchestrator) recordStepComplete(ctx context.Context, stepName string) {
	if o.bootStore != nil {
		_ = o.bootStore.MarkStep(ctx, o.spec.ClusterName, "docker", stepName, state.StatusComplete)
	}
}

func (o *DockerBootOrchestrator) markRunning(i int) { o.steps[i].Status = StatusRunning }
func (o *DockerBootOrchestrator) markFailed(i int, e error) {
	o.steps[i].Status = StatusFailed
	o.steps[i].Error = e.Error()
}
func (o *DockerBootOrchestrator) markComplete(i int, d time.Duration) {
	o.steps[i].Status = StatusComplete
	o.steps[i].Duration = d.Truncate(time.Millisecond).String()
}

func (o *DockerBootOrchestrator) emit(events chan<- Event, eventType, step, msg string) {
	if events != nil {
		events <- Event{Type: eventType, Step: step, Message: msg, Timestamp: time.Now().UnixMilli()}
	}
}

// fprintf writes to output, discarding errors (for logging only).
func fprintf(w io.Writer, format string, args ...interface{}) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// waitForHTTP polls an endpoint until it returns a successful response.
func waitForHTTP(ctx context.Context, endpoint string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(endpoint + "/healthz")
		if err == nil {
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("endpoint %s not ready after %s", endpoint, timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
