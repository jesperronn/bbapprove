// Package config resolves bbapprove's site-specific settings (forge type and URL, token item name,
// bot author email, disallow list) from flags, environment, a TOML config file and built-in defaults.
//
// Precedence per setting: command line > environment > config file > built-in default. Only `type` and
// `user_token_name` have built-in defaults; `repository` and `author_email` must be configured.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"bbapprove/internal/rules"
)

// Where a setting's value came from.
const (
	FromFlag    = "flag"
	FromEnv     = "env"
	FromFile    = "file"
	FromDefault = "default"
	FromNone    = "unset"
)

// Built-in defaults. There is deliberately none for repository or author_email.
const (
	DefaultType      = "bitbucket"
	DefaultTokenName = "BITBUCKET_TOKEN"
	// TemplateName is the committed example config; the real file is TemplateName's sibling ConfigName.
	TemplateName = "bbapprove.config.template"
	ConfigName   = "bbapprove.config.toml"
)

// knownKeys are the keys a config file may contain; anything else only draws a warning.
var knownKeys = []string{"type", "repository", "user_token_name", "author_email", "disallow"}

// fileData is the TOML file's shape.
type fileData struct {
	Type          string   `toml:"type"`
	Repository    string   `toml:"repository"`
	UserTokenName string   `toml:"user_token_name"`
	AuthorEmail   string   `toml:"author_email"`
	Disallow      []string `toml:"disallow"`
}

// Value is a resolved setting and where it came from.
type Value struct {
	Value  string
	Source string
}

// Input carries everything Load needs; no process state is read except via these fields.
type Input struct {
	Env     func(string) string // environment lookup (last-wins over Environ)
	RepoDir string              // repo root: <repo>/bbapprove.config.toml, <repo>/adapters/<type>, <repo>/disallow.txt

	// Command-line values; empty means "not given".
	ConfigFlag    string
	TokenNameFlag string
	DisallowFlag  string
}

// Config is the merged result.
type Config struct {
	Type, Repository, TokenName, AuthorEmail Value
	Adapter                                  Value // adapter executable
	Disallow                                 []string
	DisallowFile                             Value

	File     string   // config file in use ("" when none)
	Searched []string // paths looked at (for the first-run message)
	Warnings []string // e.g. unknown keys; the caller prints them to stderr
}

// ConfigHome returns $XDG_CONFIG_HOME or ~/.config as seen through env, falling back to the process.
func ConfigHome(env func(string) string) string {
	if x := env("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	home := env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

// Load finds and parses the config file and merges every source. It fails when an explicitly named
// file is missing, or the file is unreadable or malformed TOML. Missing required settings are NOT an
// error here (see Missing), so --print-config can still show what is set.
func Load(in Input) (*Config, error) {
	c := &Config{}
	explicit, explicitFrom := in.ConfigFlag, "--config"
	if explicit == "" {
		explicit, explicitFrom = in.Env("BBAPPROVE_CONFIG"), "BBAPPROVE_CONFIG"
	}
	var data fileData
	if explicit != "" {
		c.Searched = []string{explicit}
		if _, err := os.Stat(explicit); err != nil {
			return nil, fmt.Errorf("config file %s (from %s) cannot be used: %v", explicit, explicitFrom, errText(err))
		}
		c.File = explicit
	} else {
		cands := []string{filepath.Join(in.RepoDir, ConfigName)}
		if h := ConfigHome(in.Env); h != "" {
			cands = append(cands, filepath.Join(h, "bbapprove", "config.toml"))
		}
		c.Searched = cands
		for _, p := range cands {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				c.File = p
				break
			}
		}
	}
	if c.File != "" {
		var err error
		if data, err = parseFile(c.File, &c.Warnings); err != nil {
			return nil, err
		}
	}

	pick := func(flag, env, file, def string) Value {
		switch {
		case flag != "":
			return Value{flag, FromFlag}
		case env != "":
			return Value{env, FromEnv}
		case file != "":
			return Value{file, FromFile}
		case def != "":
			return Value{def, FromDefault}
		}
		return Value{"", FromNone}
	}
	// `type` has no flag or env of its own: BBAPPROVE_ADAPTER (a path) overrides what it selects.
	c.Type = pick("", "", data.Type, DefaultType)
	c.Repository = pick("", in.Env("BBAPPROVE_REPOSITORY"), data.Repository, "")
	c.TokenName = pick(in.TokenNameFlag, in.Env("BBAPPROVE_TOKEN_NAME"), data.UserTokenName, DefaultTokenName)
	c.AuthorEmail = pick("", in.Env("BBAPPROVE_AUTHOR_EMAIL"), data.AuthorEmail, "")

	if t := c.Type.Value; t == "." || t == ".." || strings.ContainsAny(t, `/\`) {
		return nil, fmt.Errorf("invalid type %q: it must be a plain adapter name (adapters/<type>)", t)
	}
	if ad := in.Env("BBAPPROVE_ADAPTER"); ad != "" {
		c.Adapter = Value{ad, FromEnv}
	} else {
		c.Adapter = Value{filepath.Join(in.RepoDir, "adapters", c.Type.Value), FromDefault}
		if c.Type.Source == FromFile {
			c.Adapter.Source = FromFile
		}
	}

	// Disallow: config file + optional disallow file + (flag, else env), all merged.
	c.DisallowFile = Value{in.Env("BBAPPROVE_DISALLOW_FILE"), FromEnv}
	if c.DisallowFile.Value == "" {
		c.DisallowFile = Value{filepath.Join(in.RepoDir, "disallow.txt"), FromDefault}
	}
	extra := in.DisallowFlag
	if extra == "" {
		extra = in.Env("BBAPPROVE_DISALLOW")
	}
	c.Disallow = rules.LoadDisallow(c.DisallowFile.Value, extra)
	for _, d := range data.Disallow {
		if n := rules.Normalize(d); n != "" {
			c.Disallow = append(c.Disallow, n)
		}
	}
	return c, nil
}

func errText(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "no such file"
	}
	return err.Error()
}

// parseFile reads and decodes one TOML file, appending unknown-key warnings.
func parseFile(path string, warnings *[]string) (fileData, error) {
	var d fileData
	b, err := os.ReadFile(path)
	if err != nil {
		return d, fmt.Errorf("cannot read config file %s: %v", path, errText(err))
	}
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil {
		return d, tomlErr(path, err)
	}
	if err := toml.Unmarshal(b, &d); err != nil {
		return d, tomlErr(path, err)
	}
	var unknown []string
	for k := range raw {
		if !contains(knownKeys, k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		*warnings = append(*warnings, fmt.Sprintf("%s: unknown key %q ignored (known: %s)", path, k, strings.Join(knownKeys, ", ")))
	}
	return d, nil
}

func tomlErr(path string, err error) error {
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Errorf("invalid TOML in %s at line %d, column %d: %s", path, row, col, firstLine(de.Error()))
	}
	return fmt.Errorf("invalid config file %s: %s", path, firstLine(err.Error()))
}

func firstLine(s string) string { return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0]) }

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Missing returns nil when repository and author_email are set, else an error naming the first
// missing setting and how to create the config file.
func (c *Config) Missing() error {
	for _, s := range []struct {
		name, env string
		v         Value
	}{
		{"repository", "BBAPPROVE_REPOSITORY", c.Repository},
		{"author_email", "BBAPPROVE_AUTHOR_EMAIL", c.AuthorEmail},
	} {
		if s.v.Value != "" {
			continue
		}
		where := "no config file found"
		if c.File != "" {
			where = "config file " + c.File + " does not set it"
		}
		return fmt.Errorf("bbapprove: %q is not set (%s).\n"+
			"Set it in a config file (or via %s): cp %s %s and edit it.\n"+
			"Searched: %s",
			s.name, where, s.env, TemplateName, ConfigName, strings.Join(c.Searched, ", "))
	}
	return nil
}

// ChildEnv returns environ with the resolved settings the adapter needs appended (BBAPPROVE_BASE_URL,
// BBAPPROVE_TOKEN_NAME); later entries win. The adapter never reads TOML.
func (c *Config) ChildEnv(environ []string) []string {
	return append(append([]string{}, environ...),
		"BBAPPROVE_BASE_URL="+c.Repository.Value,
		"BBAPPROVE_TOKEN_NAME="+c.TokenName.Value)
}

// Describe renders the effective settings and their sources. It never includes a token.
func (c *Config) Describe() string {
	var b strings.Builder
	file := c.File
	if file == "" {
		file = "(none found; searched " + strings.Join(c.Searched, ", ") + ")"
	}
	fmt.Fprintf(&b, "config file:     %s\n", file)
	row := func(name string, v Value) {
		val := v.Value
		if val == "" {
			val = "(not set)"
		}
		fmt.Fprintf(&b, "%-16s %s  [%s]\n", name+":", val, v.Source)
	}
	row("type", c.Type)
	row("adapter", c.Adapter)
	row("repository", c.Repository)
	row("user_token_name", c.TokenName)
	row("author_email", c.AuthorEmail)
	row("disallow_file", c.DisallowFile)
	fmt.Fprintf(&b, "%-16s %s\n", "disallow:", strings.Join(c.Disallow, ", "))
	return b.String()
}
