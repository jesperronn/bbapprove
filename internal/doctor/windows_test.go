package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests report GOOS "windows" and run on every OS: Windows files have no permission bits, so
// doctor must neither complain that the adapter is not executable nor print a file mode.
func TestWindowsIgnoresModes(t *testing.T) {
	r := newRigAnyOS(t)
	r.writeXDG(validCfg)
	os.Chmod(r.writeXDG(validCfg), 0o666) // a "loose" mode that would be noted on Unix
	os.WriteFile(r.ignorePath(), []byte("left-pad\n"), 0o666)
	ad := filepath.Join(r.root, "adapters", "bitbucket")
	os.Chmod(ad, 0o644) // not executable
	code, out := r.doctorV(func(o *Options) { o.GOOS = "windows"; o.Offline = true })
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	mustNotContain(t, out, "not executable", "chmod", "mode 0", "default 0600")
	mustContain(t, out, "1 entry")
	mustNotContain(t, out, "1 entry, 0")
}

func TestWindowsLabelsTheUserConfig(t *testing.T) {
	r := newRigAnyOS(t)
	r.env = []string{"NO_COLOR=1", "APPDATA=" + r.xdg, "BBAPPROVE_TOKEN=tok-" + canary}
	appdata := filepath.Join(r.xdg, "bbapprove", "config.toml")
	os.MkdirAll(filepath.Dir(appdata), 0o755)
	os.WriteFile(appdata, []byte(validCfg), 0o600)
	t.Setenv("XDG_CONFIG_HOME", "")
	_, out := r.doctorV(func(o *Options) { o.GOOS = "windows"; o.Offline = true })
	mustContain(t, out, appdata+" (AppData config)", "4. AppData config")
	mustNotContain(t, out, "XDG config")

	r.env = append(r.env, "XDG_CONFIG_HOME="+r.xdg)
	_, out = r.doctorV(func(o *Options) { o.GOOS = "windows"; o.Offline = true })
	mustContain(t, out, "(XDG config)")
}
