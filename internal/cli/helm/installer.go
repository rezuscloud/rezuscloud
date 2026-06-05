package helm

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rezuscloud/rezuscloud/internal/cli/provider"
)

const defaultHelmDriver = "secret"

// HelmInstaller implements provider.ChartInstaller using the Helm Go SDK.
type HelmInstaller struct {
	settings *cli.EnvSettings
}

// NewInstaller creates a Helm installer from a kubeconfig path.
func NewInstaller(kubeconfigPath string) (*HelmInstaller, error) {
	settings := cli.New()
	if kubeconfigPath != "" {
		settings.KubeConfig = kubeconfigPath
	}
	return &HelmInstaller{settings: settings}, nil
}

// NewInstallerFromBytes creates a Helm installer from kubeconfig content.
func NewInstallerFromBytes(kubeconfig []byte) (*HelmInstaller, error) {
	settings := cli.New()
	tmpFile, err := WriteTempKubeconfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("write temp kubeconfig: %w", err)
	}
	settings.KubeConfig = tmpFile

	return &HelmInstaller{settings: settings}, nil
}

// NewInstallerFromRestConfig creates a Helm installer from a Kubernetes rest.Config.
// It writes a temp kubeconfig derived from the rest.Config for Helm's RESTClientGetter.
func NewInstallerFromRestConfig(cfg *rest.Config) (*HelmInstaller, error) {
	kubeconfig, err := RestConfigToKubeconfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("convert rest config: %w", err)
	}
	return NewInstallerFromBytes(kubeconfig)
}

// restConfigToKubeconfig converts a rest.Config to kubeconfig YAML bytes.
// Exported for testing.
func RestConfigToKubeconfig(cfg *rest.Config) ([]byte, error) {
	clusterName := "rezuscloud"
	userName := "rezusctl"

	clust := clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	if cfg.Insecure {
		clust.InsecureSkipTLSVerify = true
	}

	auth := clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
		Token:                 cfg.BearerToken,
	}

	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: &clust,
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			userName: &auth,
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {
				Cluster:  clusterName,
				AuthInfo: userName,
			},
		},
		CurrentContext: "default",
	}

	return clientcmd.Write(kubeconfig)
}

func (h *HelmInstaller) newActionConfig(namespace string) (*action.Configuration, error) {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(h.settings.RESTClientGetter(), namespace, defaultHelmDriver, log.Printf); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}
	return actionConfig, nil
}

// Install installs or upgrades a Helm chart.
func (h *HelmInstaller) Install(ctx context.Context, config provider.ChartConfig, out io.Writer) error {
	actionConfig, err := h.newActionConfig(config.Namespace)
	if err != nil {
		return err
	}

	chartPath, err := h.locateChart(config)
	if err != nil {
		return fmt.Errorf("locate chart %s: %w", config.Chart, err)
	}

	chartObj, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("load chart %s: %w", chartPath, err)
	}

	if err := checkChartDependencies(chartObj); err != nil {
		return fmt.Errorf("chart dependencies: %w", err)
	}

	installed, err := h.IsInstalled(ctx, config.Name, config.Namespace)
	if err != nil {
		return fmt.Errorf("check installed: %w", err)
	}

	if installed {
		return h.upgrade(ctx, actionConfig, config, chartObj)
	}

	return h.install(ctx, actionConfig, config, chartObj)
}

func (h *HelmInstaller) install(ctx context.Context, actionConfig *action.Configuration, config provider.ChartConfig, chartObj *chart.Chart) error {
	inst := action.NewInstall(actionConfig)
	inst.ReleaseName = config.Name
	inst.Namespace = config.Namespace
	inst.Version = config.Version
	inst.Wait = config.Wait
	inst.Timeout = time.Duration(config.Timeout) * time.Second
	inst.CreateNamespace = true
	inst.DisableHooks = config.DisableHooks

	_, err := inst.RunWithContext(ctx, chartObj, config.Values)
	if err != nil {
		return fmt.Errorf("helm install %s: %w", config.Name, err)
	}
	return nil
}

func (h *HelmInstaller) upgrade(ctx context.Context, actionConfig *action.Configuration, config provider.ChartConfig, chartObj *chart.Chart) error {
	upg := action.NewUpgrade(actionConfig)
	upg.Namespace = config.Namespace
	upg.Version = config.Version
	upg.Wait = config.Wait
	upg.Timeout = time.Duration(config.Timeout) * time.Second
	upg.ReuseValues = true
	upg.DisableHooks = config.DisableHooks

	_, err := upg.RunWithContext(ctx, config.Name, chartObj, config.Values)
	if err != nil {
		return fmt.Errorf("helm upgrade %s: %w", config.Name, err)
	}
	return nil
}

// Rollback rolls back a stuck Helm release.
func (h *HelmInstaller) Rollback(ctx context.Context, releaseName, namespace string) error {
	actionConfig, err := h.newActionConfig(namespace)
	if err != nil {
		return err
	}

	hist := action.NewHistory(actionConfig)
	releases, err := hist.Run(releaseName)
	if err != nil {
		return fmt.Errorf("helm history %s: %w", releaseName, err)
	}

	if len(releases) < 2 {
		return fmt.Errorf("no previous release to rollback to for %s", releaseName)
	}

	lastDeployed := releases[len(releases)-1]
	if lastDeployed.Info.Status != release.StatusPendingUpgrade && lastDeployed.Info.Status != release.StatusPendingInstall {
		return nil
	}

	previousRevision := releases[len(releases)-2].Version
	rollback := action.NewRollback(actionConfig)
	rollback.Version = previousRevision
	rollback.Wait = true
	rollback.Timeout = 300 * time.Second

	if err := rollback.Run(releaseName); err != nil {
		return fmt.Errorf("helm rollback %s to %d: %w", releaseName, previousRevision, err)
	}
	return nil
}

// IsInstalled checks whether a Helm release exists.
func (h *HelmInstaller) IsInstalled(ctx context.Context, releaseName, namespace string) (bool, error) {
	actionConfig, err := h.newActionConfig(namespace)
	if err != nil {
		return false, err
	}

	get := action.NewGet(actionConfig)
	_, err = get.Run(releaseName)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (h *HelmInstaller) locateChart(config provider.ChartConfig) (string, error) {
	// Use an isolated temp directory for Helm settings to avoid picking up
	// local chart directories (e.g. a cilium/ fork checkout in the CWD).
	repoDir, err := os.MkdirTemp("", "rezusctl-helm-repo-*")
	if err != nil {
		return "", fmt.Errorf("create temp repo dir: %w", err)
	}

	cacheDir := filepath.Join(repoDir, "cache")

	settings := cli.New()
	settings.KubeConfig = h.settings.KubeConfig
	settings.RepositoryCache = cacheDir
	settings.RepositoryConfig = filepath.Join(repoDir, "repositories.yaml")

	// Download and cache the repo index.
	repoEntry := &repo.Entry{Name: config.Name, URL: config.Repository}
	chartRepo, err := repo.NewChartRepository(repoEntry, getter.All(settings))
	if err != nil {
		return "", fmt.Errorf("create chart repository: %w", err)
	}
	chartRepo.CachePath = cacheDir
	if _, err := chartRepo.DownloadIndexFile(); err != nil {
		return "", fmt.Errorf("download repo index: %w", err)
	}

	repoFile := repo.NewFile()
	repoFile.Add(repoEntry)
	_ = repoFile.WriteFile(settings.RepositoryConfig, 0o644)

	cd := &downloader.ChartDownloader{
		Out:              os.Stderr,
		Keyring:          defaultKeyring(),
		Getters:          getter.All(settings),
		Options:          []getter.Option{},
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
	}

	chartRef := config.Name + "/" + config.Chart
	saved, _, err := cd.DownloadTo(chartRef, config.Version, repoDir)
	if err != nil {
		return "", fmt.Errorf("download chart %s version %s: %w", chartRef, config.Version, err)
	}

	return saved, nil
}

func checkChartDependencies(chartObj *chart.Chart) error {
	if req := chartObj.Metadata.Dependencies; req != nil {
		if err := action.CheckDependencies(chartObj, req); err != nil {
			return err
		}
	}
	return nil
}

// defaultKeyring returns the default keyring path for chart verification.
func defaultKeyring() string {
	if v := os.Getenv("GNUPGHOME"); v != "" {
		return filepath.Join(v, "pubring.gpg")
	}
	return filepath.Join(os.Getenv("HOME"), ".gnupg", "pubring.gpg")
}

// WriteTempKubeconfig writes kubeconfig data to a temp file and returns the path.
// Exported for testing.
func WriteTempKubeconfig(data []byte) (string, error) {
	tmpDir, err := os.MkdirTemp("", "rezusctl-kubeconfig-*")
	if err != nil {
		return "", err
	}
	path := tmpDir + "/kubeconfig"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}
	return path, nil
}
