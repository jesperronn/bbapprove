// Package userconfig decides where bbapprove's per-user files (the config file and the ignore list) live.
//
// The directory is $XDG_CONFIG_HOME/bbapprove when XDG_CONFIG_HOME is set (on any OS), else on Windows
// %AppData%\bbapprove (os.UserConfigDir), else ~/.config/bbapprove. It is a package of its own so that
// both internal/config and internal/ignore can use it without importing each other.
package userconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Dir is the per-user directory's name below the config home.
const Dir = "bbapprove"

// HomeFor returns the config home (the directory that holds Dir) for goos, or "" when none can be
// determined. env reads the variables a caller injects; XDG_CONFIG_HOME falls back to the process
// environment. userConfigDir and userHomeDir are os.UserConfigDir and os.UserHomeDir (seams for tests).
func HomeFor(goos string, env func(string) string, userConfigDir, userHomeDir func() (string, error)) string {
	if x := env("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	if goos == "windows" {
		if a := env("APPDATA"); a != "" {
			return a
		}
		if d, err := userConfigDir(); err == nil && d != "" {
			return d
		}
		return ""
	}
	home := env("HOME")
	if home == "" {
		home, _ = userHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

// Home is HomeFor for the running OS and process.
func Home(env func(string) string) string { return HomeOn("", env) }

// HomeOn is Home as goos sees it (empty means the running OS), for callers that report on another OS.
func HomeOn(goos string, env func(string) string) string {
	if goos == "" {
		goos = runtime.GOOS
	}
	return HomeFor(goos, env, os.UserConfigDir, os.UserHomeDir)
}

// Path is the file name inside the per-user directory (for example "config.toml"), or "" when there is no config home.
func Path(env func(string) string, name string) string {
	h := Home(env)
	if h == "" {
		return ""
	}
	return filepath.Join(h, Dir, name)
}

// DefaultHome is the config home as help text spells it when XDG_CONFIG_HOME is not set.
func DefaultHome(goos string) string {
	if goos == "windows" {
		return `%AppData%`
	}
	return "~/.config"
}

// Display is where the file called name lives, as help text spells it: the XDG form and the default
// form, with the separator of goos. For example "$XDG_CONFIG_HOME/bbapprove/config.toml" and
// "~/.config/bbapprove/config.toml".
func Display(goos, name string) (xdg, def string) {
	xdg = "$XDG_CONFIG_HOME/" + Dir + "/" + name
	def = DefaultHome(goos) + "/" + Dir + "/" + name
	if goos == "windows" {
		xdg = strings.ReplaceAll(xdg, "/", `\`)
		def = strings.ReplaceAll(def, "/", `\`)
	}
	return xdg, def
}
