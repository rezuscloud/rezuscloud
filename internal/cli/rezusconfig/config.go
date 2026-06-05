// Package rezusconfig manages the rezusctl configuration file (~/.rezuscloud/config).
package rezusconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the rezusctl configuration.
type Config struct {
	CurrentContext string    `yaml:"current-context"`
	Contexts       []Context `yaml:"contexts"`
}

// Context is a named connection to a management plane.
type Context struct {
	Name  string `yaml:"name"`
	URL   string `yaml:"url"`
	Token string `yaml:"token,omitempty"`
}

// DefaultPath returns the default config file path.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".rezuscloud", "config"), nil
}

// Load reads the config file from disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}

// Save writes the config file to disk.
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// Current returns the current context, or nil if none is set.
func (c *Config) Current() *Context {
	for i := range c.Contexts {
		if c.Contexts[i].Name == c.CurrentContext {
			return &c.Contexts[i]
		}
	}
	return nil
}

// GetContext returns a context by name.
func (c *Config) GetContext(name string) *Context {
	for i := range c.Contexts {
		if c.Contexts[i].Name == name {
			return &c.Contexts[i]
		}
	}
	return nil
}

// SetURL updates the URL for the current context.
func (c *Config) SetURL(url string) {
	ctx := c.Current()
	if ctx != nil {
		ctx.URL = url
		return
	}

	// No context yet, create default.
	c.Contexts = append(c.Contexts, Context{
		Name: "default",
		URL:  url,
	})
	c.CurrentContext = "default"
}

// SetToken updates the token for the current context.
func (c *Config) SetToken(token string) {
	ctx := c.Current()
	if ctx != nil {
		ctx.Token = token
	}
}

// SwitchContext changes the current context.
func (c *Config) SwitchContext(name string) error {
	if c.GetContext(name) == nil {
		return fmt.Errorf("context %q not found", name)
	}
	c.CurrentContext = name
	return nil
}
