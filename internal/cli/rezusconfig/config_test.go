package rezusconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	cfg := &Config{
		CurrentContext: "default",
		Contexts: []Context{
			{Name: "default", URL: "http://localhost:8080", Token: "test-token"},
		},
	}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.CurrentContext != "default" {
		t.Errorf("current context = %q, want default", loaded.CurrentContext)
	}
	if len(loaded.Contexts) != 1 {
		t.Fatalf("contexts = %d, want 1", len(loaded.Contexts))
	}
	if loaded.Contexts[0].URL != "http://localhost:8080" {
		t.Errorf("url = %q, want http://localhost:8080", loaded.Contexts[0].URL)
	}
	if loaded.Contexts[0].Token != "test-token" {
		t.Errorf("token = %q, want test-token", loaded.Contexts[0].Token)
	}
}

func TestConfig_LoadNonexistent(t *testing.T) {
	cfg, err := Load("/nonexistent/config")
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if cfg.CurrentContext != "" {
		t.Errorf("expected empty config, got current context %q", cfg.CurrentContext)
	}
}

func TestConfig_Current(t *testing.T) {
	cfg := &Config{
		CurrentContext: "prod",
		Contexts: []Context{
			{Name: "default", URL: "http://localhost:8080"},
			{Name: "prod", URL: "https://manage.rezus.cloud"},
		},
	}

	ctx := cfg.Current()
	if ctx == nil {
		t.Fatal("Current() returned nil")
	}
	if ctx.URL != "https://manage.rezus.cloud" {
		t.Errorf("Current().URL = %q, want https://manage.rezus.cloud", ctx.URL)
	}
}

func TestConfig_SetURL_ExistingContext(t *testing.T) {
	cfg := &Config{
		CurrentContext: "default",
		Contexts: []Context{
			{Name: "default", URL: "http://old:8080"},
		},
	}

	cfg.SetURL("http://new:9090")
	if cfg.Contexts[0].URL != "http://new:9090" {
		t.Errorf("URL = %q, want http://new:9090", cfg.Contexts[0].URL)
	}
}

func TestConfig_SetURL_NoContext(t *testing.T) {
	cfg := &Config{}

	cfg.SetURL("http://localhost:8080")
	if cfg.CurrentContext != "default" {
		t.Errorf("current context = %q, want default", cfg.CurrentContext)
	}
	if len(cfg.Contexts) != 1 {
		t.Fatalf("contexts = %d, want 1", len(cfg.Contexts))
	}
	if cfg.Contexts[0].URL != "http://localhost:8080" {
		t.Errorf("URL = %q, want http://localhost:8080", cfg.Contexts[0].URL)
	}
}

func TestConfig_SwitchContext(t *testing.T) {
	cfg := &Config{
		CurrentContext: "default",
		Contexts: []Context{
			{Name: "default", URL: "http://localhost:8080"},
			{Name: "prod", URL: "https://manage.rezus.cloud"},
		},
	}

	if err := cfg.SwitchContext("prod"); err != nil {
		t.Fatalf("SwitchContext: %v", err)
	}
	if cfg.CurrentContext != "prod" {
		t.Errorf("current = %q, want prod", cfg.CurrentContext)
	}

	if err := cfg.SwitchContext("unknown"); err == nil {
		t.Error("expected error for unknown context")
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".rezuscloud", "config")
	if path != expected {
		t.Errorf("DefaultPath() = %q, want %q", path, expected)
	}
}

func TestConfig_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config")

	cfg := &Config{
		CurrentContext: "default",
		Contexts:       []Context{{Name: "default", URL: "http://localhost:8080"}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save with mkdir: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}
}
