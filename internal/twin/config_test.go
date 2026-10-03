package twin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigErrors(t *testing.T) {
	f := filepath.Join(t.TempDir(), "cfg.yaml")
	os.WriteFile(f, []byte("key: [unclosed\n"), 0o644)
	t.Setenv("TWIN_CONFIG", f)
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "config syntax error") {
		t.Errorf("invalid yaml: %v", err)
	}

	cfg := NewConfig()
	cfg.SyncDir = "/no/such/dir"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sync_dir not found") {
		t.Errorf("missing sync_dir: %v", err)
	}
}

func TestConfig(t *testing.T) {
	if got := NewConfig().GlobalExcludes; !eq(got, []string{".DS_Store"}) {
		t.Errorf("defaults: %v", got)
	}

	f := filepath.Join(t.TempDir(), "cfg.yaml")
	os.WriteFile(f, []byte("sync_dir: /x\nglobal_excludes: [foo]\n"), 0o644)
	t.Setenv("TWIN_CONFIG", f)
	cfg, err := LoadConfig()
	if err != nil || cfg.SyncDir != "/x" || !eq(cfg.GlobalExcludes, []string{"foo"}) {
		t.Errorf("data overrides defaults: %+v %v", cfg, err)
	}

	t.Setenv("TWIN_SYNC_DIR", "/y")
	cfg, _ = LoadConfig()
	if cfg.SyncDir != "/y" {
		t.Errorf("env overrides sync_dir: %s", cfg.SyncDir)
	}
	if NewConfig().SyncDir != "/y" {
		t.Error("env applies to fresh config too")
	}
}

func varCfg() *Config {
	cfg := NewConfig()
	cfg.SyncDir = "/tmp"
	cfg.Hosts = map[string]map[string]string{
		"mini": {"home": "/Volumes/ext", "git": "/Volumes/git"},
		"book": {"home": "/Users/ralf", "git": "/Users/ralf/git", "mount": "/Volumes/ralf"},
	}
	cfg.Host, cfg.Target = "mini", "book"
	return cfg
}

func TestConfigVarMap(t *testing.T) {
	m, err := varCfg().VarMap()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"src.home": "/Volumes/ext", "src.git": "/Volumes/git",
		"dst.home": "/Users/ralf", "dst.git": "/Users/ralf/git", "dst.mount": "/Volumes/ralf",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}

	plain := NewConfig()
	plain.SyncDir = "/tmp"
	if m, err := plain.VarMap(); err != nil || len(m) != 0 {
		t.Errorf("empty when no hosts: %v %v", m, err)
	}

	c := varCfg()
	c.Host = "unknown"
	if _, err := c.VarMap(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("missing host: %v", err)
	}
	c = varCfg()
	c.Target = "unknown"
	if _, err := c.VarMap(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("missing target: %v", err)
	}
	c = varCfg()
	c.Host = ""
	if _, err := c.VarMap(); err == nil || !strings.Contains(err.Error(), "host not set") {
		t.Errorf("host not set: %v", err)
	}
}

func TestTemplate(t *testing.T) {
	vars := map[string]string{"src.home": "/Volumes/src", "dst.home": "/Users/ralf", "dst.mount": "/Volumes/ralf"}
	cases := map[string]string{
		"plain string":                       "plain string",
		"{{src.home}}/foo":                   "/Volumes/src/foo",
		"{{dst.mount}}/x and {{dst.home}}/y": "/Volumes/ralf/x and /Users/ralf/y",
	}
	for in, want := range cases {
		if got, err := Substitute(in, vars, "test"); err != nil || got != want {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	_, err := Substitute("{{unknown}}", vars, "ctx")
	if err == nil || !strings.Contains(err.Error(), "{{unknown}}") || !strings.Contains(err.Error(), "ctx") {
		t.Errorf("unknown token: %v", err)
	}

	r := map[string]any{"Source": "{{src.home}}/sync", "Target": "{{dst.mount}}", "Path": "foo"}
	out, err := SubstituteRecord(r, vars, "test")
	if err != nil || out["Source"] != "/Volumes/src/sync" || out["Target"] != "/Volumes/ralf" || out["Path"] != "foo" {
		t.Errorf("record: %v %v", out, err)
	}
	if out["Source"] != "/Volumes/src/sync" || r["Source"] != "{{src.home}}/sync" {
		t.Error("record substitution must not mutate the input")
	}
	plain := map[string]any{"Source": "/abs/path", "Target": "/other"}
	if out, _ := SubstituteRecord(plain, vars, "test"); out["Source"] != "/abs/path" {
		t.Error("noop without tokens")
	}
	tok := map[string]any{"Source": "{{src.home}}/x"}
	if out, err := SubstituteRecord(tok, map[string]string{}, "test"); err != nil || out["Source"] != "{{src.home}}/x" {
		t.Error("noop with empty vars")
	}
	if _, err := SubstituteRecord(map[string]any{"Source": "{{oops}}/path"}, vars, "test"); err == nil {
		t.Error("record raises on unknown token")
	}
}
