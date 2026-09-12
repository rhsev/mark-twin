package twin

import (
	"strings"
	"testing"
	"time"
)

func TestRemote(t *testing.T) {
	for _, tgt := range []string{"ralf@server:/srv/www", "server:/srv/www"} {
		if !IsRemote(tgt) {
			t.Errorf("%q must be remote", tgt)
		}
	}
	for _, tgt := range []string{"/Volumes/ralf/git", "/tmp/a:b", ""} {
		if IsRemote(tgt) {
			t.Errorf("%q must not be remote", tgt)
		}
	}
	if h, p := SplitRemote("ralf@server:/srv/www"); h != "ralf@server" || p != "/srv/www" {
		t.Errorf("split: %q %q", h, p)
	}
	if h, p := SplitRemote("server:/a:b"); h != "server" || p != "/a:b" {
		t.Errorf("split keeps later colons: %q %q", h, p)
	}
	if got := ShellEsc("/a dir/it's"); got != `'/a dir/it'\''s'` {
		t.Errorf("shellesc: %s", got)
	}
	// Both batch scripts travel to the far side wrapped in '...' — a single
	// quote inside would end the wrapping early.
	for name, s := range map[string]string{"stat": StatScript, "md5": MD5Script, "preflight": PreflightScript} {
		if strings.Contains(s, "'") {
			t.Errorf("%s script contains a single quote", name)
		}
	}
	got := ParsePreflight("rsync\tok\nstat\t-\ndate\tok\nmd5\t-\nmd5sum\tok\n")
	want := map[string]bool{"rsync": true, "stat": false, "date": true, "md5": false, "md5sum": true}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("preflight %s = %v", k, got[k])
		}
	}
}

// "-" is a missing file; an empty mtime is a file whose whole stat chain
// failed — unknown, not missing (and not 1970).
func TestRemoteStatParsing(t *testing.T) {
	stats := ParseStats("/a\t-\n/b\t\n/c\t1756500000\n")
	if stats["/a"].Exists {
		t.Error("/a must be missing")
	}
	b, ok := stats["/b"]
	if !ok || !b.Exists || b.Mtime != nil {
		t.Errorf("/b must be present with unknown mtime: %v %+v", ok, b)
	}
	c := stats["/c"]
	if !c.Exists || c.Mtime == nil || !c.Mtime.Equal(time.Unix(1756500000, 0)) {
		t.Errorf("/c: %+v", c)
	}
	if g := ParseStats("/a\t1970-01-01\n")["/a"]; !g.Exists || g.Mtime != nil {
		t.Errorf("garbage mtime reads as unknown: %+v", g)
	}
}
