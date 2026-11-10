package userconfig

import (
	"errors"
	"path/filepath"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestHomeFor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgDir := func() (string, error) { return `C:\Users\me\AppData\Roaming`, nil }
	noCfgDir := func() (string, error) { return "", errors.New("no %AppData%") }
	homeDir := func() (string, error) { return "/home/me", nil }
	noHome := func() (string, error) { return "", errors.New("no home") }

	for _, tc := range []struct {
		name string
		goos string
		env  map[string]string
		cfg  func() (string, error)
		home func() (string, error)
		want string
	}{
		{"unix default", "linux", nil, cfgDir, homeDir, filepath.Join("/home/me", ".config")},
		{"unix HOME from env wins over the process", "darwin", map[string]string{"HOME": "/h"}, cfgDir, homeDir, filepath.Join("/h", ".config")},
		{"unix XDG wins", "linux", map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"}, cfgDir, homeDir, "/x"},
		{"unix no home", "linux", nil, cfgDir, noHome, ""},
		{"windows default is AppData", "windows", nil, cfgDir, homeDir, `C:\Users\me\AppData\Roaming`},
		{"windows APPDATA from env", "windows", map[string]string{"APPDATA": `D:\roam`}, cfgDir, homeDir, `D:\roam`},
		{"windows XDG wins", "windows", map[string]string{"XDG_CONFIG_HOME": `D:\xdg`, "APPDATA": `D:\roam`}, cfgDir, homeDir, `D:\xdg`},
		{"windows ignores HOME", "windows", map[string]string{"HOME": `C:\h`}, cfgDir, homeDir, `C:\Users\me\AppData\Roaming`},
		{"windows without AppData", "windows", nil, noCfgDir, homeDir, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HomeFor(tc.goos, envOf(tc.env), tc.cfg, tc.home); got != tc.want {
				t.Errorf("HomeFor = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHomeForUsesProcessXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/proc/xdg")
	got := HomeFor("windows", envOf(nil), func() (string, error) { return "/a", nil }, func() (string, error) { return "/h", nil })
	if got != "/proc/xdg" {
		t.Errorf("HomeFor = %q, want the process XDG_CONFIG_HOME", got)
	}
}

func TestDisplay(t *testing.T) {
	for _, tc := range []struct{ goos, xdg, def string }{
		{"linux", "$XDG_CONFIG_HOME/bbapprove/config.toml", "~/.config/bbapprove/config.toml"},
		{"darwin", "$XDG_CONFIG_HOME/bbapprove/config.toml", "~/.config/bbapprove/config.toml"},
		{"windows", `$XDG_CONFIG_HOME\bbapprove\config.toml`, `%AppData%\bbapprove\config.toml`},
	} {
		xdg, def := Display(tc.goos, "config.toml")
		if xdg != tc.xdg || def != tc.def {
			t.Errorf("%s: Display = %q, %q; want %q, %q", tc.goos, xdg, def, tc.xdg, tc.def)
		}
	}
}
