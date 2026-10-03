package twin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is ~/.config/twin/config.yaml plus the environment overrides.
type Config struct {
	SyncDir        string
	GlobalExcludes []string
	// The apex fields are handed to the preview renderer as written; twin
	// itself never interprets them.
	ApexTheme              any
	ApexWidth              any
	ApexCodeHighlight      any
	ApexCodeHighlightTheme any
	Hosts                  map[string]map[string]string
	Host                   string
	Target                 string
}

// configFile is the on-disk shape. Pointers tell an absent key from an
// explicit value, so defaults apply only where the file says nothing.
type configFile struct {
	SyncDir                string                       `yaml:"sync_dir"`
	GlobalExcludes         *[]string                    `yaml:"global_excludes"`
	ApexTheme              any                          `yaml:"apex_theme"`
	ApexWidth              any                          `yaml:"apex_width"`
	ApexCodeHighlight      any                          `yaml:"apex_code_highlight"`
	ApexCodeHighlightTheme any                          `yaml:"apex_code_highlight_theme"`
	Hosts                  map[string]map[string]string `yaml:"hosts"`
	Host                   string                       `yaml:"host"`
	Target                 string                       `yaml:"target"`
}

// NewConfig returns the defaults with environment overrides applied.
func NewConfig() *Config {
	c := &Config{
		GlobalExcludes: []string{".DS_Store"},
		Hosts:          map[string]map[string]string{},
	}
	c.applyEnv()
	return c
}

func (c *Config) applyEnv() {
	if v := os.Getenv("TWIN_SYNC_DIR"); v != "" {
		c.SyncDir = v
	}
	if v := os.Getenv("TWIN_HOST"); v != "" {
		c.Host = v
	}
}

// ConfigPath is $TWIN_CONFIG or ~/.config/twin/config.yaml.
func ConfigPath() string {
	if v := os.Getenv("TWIN_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "twin", "config.yaml")
}

// LoadConfig reads the config file when it exists; a missing file means
// defaults. Environment variables win over the file.
func LoadConfig() (*Config, error) {
	path := ConfigPath()
	c := &Config{
		GlobalExcludes: []string{".DS_Store"},
		Hosts:          map[string]map[string]string{},
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var f configFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("config syntax error in %s: %v", path, err)
		}
		c.SyncDir = f.SyncDir
		if f.GlobalExcludes != nil {
			c.GlobalExcludes = *f.GlobalExcludes
		}
		c.ApexTheme = f.ApexTheme
		c.ApexWidth = f.ApexWidth
		c.ApexCodeHighlight = f.ApexCodeHighlight
		c.ApexCodeHighlightTheme = f.ApexCodeHighlightTheme
		if f.Hosts != nil {
			c.Hosts = f.Hosts
		}
		c.Host = f.Host
		c.Target = f.Target
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	c.applyEnv()
	return c, nil
}

// VarMap builds the flat substitution map {src.home => …, dst.mount => …}.
// Empty when no hosts are configured (substitution becomes a no-op).
func (c *Config) VarMap() (map[string]string, error) {
	m := map[string]string{}
	if len(c.Hosts) == 0 {
		return m, nil
	}
	if c.Host == "" {
		return nil, errors.New("host not set in config")
	}
	if c.Target == "" {
		return nil, errors.New("target not set in config")
	}
	src, ok := c.Hosts[c.Host]
	if !ok {
		return nil, fmt.Errorf("unknown host %q (not in hosts)", c.Host)
	}
	dst, ok := c.Hosts[c.Target]
	if !ok {
		return nil, fmt.Errorf("unknown target %q (not in hosts)", c.Target)
	}
	for k, v := range src {
		m["src."+k] = v
	}
	for k, v := range dst {
		m["dst."+k] = v
	}
	return m, nil
}

// Validate checks that the sync directory is set and exists.
func (c *Config) Validate() error {
	if c.SyncDir == "" {
		return errors.New("sync_dir not set (add to ~/.config/twin/config.yaml or set TWIN_SYNC_DIR)")
	}
	st, err := os.Stat(c.SyncDir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("sync_dir not found: %s", c.SyncDir)
	}
	return nil
}
