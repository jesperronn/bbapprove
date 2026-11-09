// Package config resolves bbapprove's site-specific settings (forge type and URL, token item name,
// bot author email, disallow list) from flags, environment, a TOML config file and built-in defaults.
//
// Precedence per setting: command line > environment > config file > built-in default. Only `type`
// has a built-in default; `repository`, `author_email` and `user_token_name` must be configured (the latter only when the token is not in BBAPPROVE_TOKEN).
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
	"bbapprove/internal/style"
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
	DefaultType = "bitbucket"
	// DefaultUpdateRepo is where --outdated/--update look for releases (owner/repo).
	DefaultUpdateRepo = "jesperronn/bbapprove"
	// TemplateName is the committed example config; the real file is TemplateName's sibling ConfigName.
	TemplateName = "bbapprove.config.template"
	ConfigName   = "bbapprove.config.toml"
)

// knownKeys are the keys a config file may contain; anything else only draws a warning.
var knownKeys = []string{"type", "repository", "user_token_name", "author_email", "disallow", "update_repo"}

// fileData is the TOML file's shape.
type fileData struct {
	Type          string   `toml:"type"`
	Repository    string   `toml:"repository"`
	UserTokenName string   `toml:"user_token_name"`
	AuthorEmail   string   `toml:"author_email"`
	Disallow      []string `toml:"disallow"`
	UpdateRepo    string   `toml:"update_repo"`
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
	UpdateRepo                               Value // GitHub owner/repo for --outdated/--update
	Adapter                                  Value // adapter executable
	Disallow                                 []string
	FileDisallow                             []string // the `disallow` entries from the config file alone
	DisallowFile                             Value

	File        string   // config file in use ("" when none)
	DefaultPath string   // where first-run setup would create it
	Searched    []string // paths looked at (for the first-run message)
	Warnings    []string // e.g. unknown keys; the caller prints them to stderr
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
	c.DefaultPath = DefaultPath(in.Env)
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
	c.TokenName = pick(in.TokenNameFlag, in.Env("BBAPPROVE_TOKEN_NAME"), data.UserTokenName, "")
	c.AuthorEmail = pick("", in.Env("BBAPPROVE_AUTHOR_EMAIL"), data.AuthorEmail, "")
	c.UpdateRepo = pick("", in.Env("BBAPPROVE_UPDATE_REPO"), data.UpdateRepo, DefaultUpdateRepo)

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
			c.FileDisallow = append(c.FileDisallow, n)
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
		msg := fmt.Sprintf("bbapprove: %q is not set (%s).\n"+
			"Set it in a config file (or via %s): cp %s %s and edit it.\n"+
			"Searched: %s",
			s.name, where, s.env, TemplateName, ConfigName, strings.Join(c.Searched, ", "))
		if c.File == "" {
			if def := c.DefaultPath; def != "" {
				msg += "\nOr create it in the default location (kept across upgrades):\n" + createHint(def)
			}
		}
		return errors.New(strings.TrimRight(msg, "\n"))
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
func (c *Config) Describe() string { return c.DescribeThemed(style.Theme{}) }

// DescribeThemed is Describe with colour: labels dim, paths cyan, sources by origin. With the zero
// Theme the text is byte-identical to the plain form.
func (c *Config) DescribeThemed(t style.Theme) string {
	var b strings.Builder
	file := t.Paint(style.Path, c.File)
	if c.File == "" {
		var searched []string
		for _, s := range c.Searched {
			searched = append(searched, t.Paint(style.Path, s))
		}
		file = t.Paint(style.Dim, "(none found; searched ") + strings.Join(searched, t.Paint(style.Dim, ", ")) + t.Paint(style.Dim, ")")
	}
	fmt.Fprintf(&b, "%s%s\n", t.Paint(style.Dim, "config file:")+"     ", file)
	if c.File == "" {
		fmt.Fprintf(&b, "                 %s\n", t.Paint(style.Dim, "no config file found: create one from the template with: bbapprove --init"))
	}
	return b.String() + c.SettingsThemed(t)
}

// Settings renders the effective settings table (one row per setting with its source). No secrets.
func (c *Config) Settings() string { return c.SettingsThemed(style.Theme{}) }

// SettingsThemed is Settings with colour: setting names dim, values in the default colour, "[source]"
// coloured by origin (flag, env, file, default; anything else dim).
func (c *Config) SettingsThemed(t style.Theme) string {
	var b strings.Builder
	label := func(name string) string {
		l := name + ":"
		return t.Paint(style.Dim, l) + strings.Repeat(" ", max(16-len(l), 0)) + " "
	}
	row := func(name string, v Value) {
		val := v.Value
		if val == "" {
			val = "(not set)"
		}
		fmt.Fprintf(&b, "%s%s  %s\n", label(name), val, t.Source(v.Source))
	}
	row("type", c.Type)
	row("adapter", c.Adapter)
	row("repository", c.Repository)
	row("user_token_name", c.TokenName)
	row("author_email", c.AuthorEmail)
	row("update_repo", c.UpdateRepo)
	row("disallow_file", c.DisallowFile)
	fmt.Fprintf(&b, "%s%s\n", label("disallow"), strings.Join(c.Disallow, ", "))
	return b.String()
}

// TokenMissingHelp is printed when neither BBAPPROVE_TOKEN nor a 1Password item name is set.
const TokenMissingHelp = `bbapprove: no API token.
Set BBAPPROVE_TOKEN to a Bitbucket HTTP access token:
  export BBAPPROVE_TOKEN=...
or name a 1Password item holding it (looked up with "op", field token, then credential):
  export BBAPPROVE_TOKEN_NAME=<item>     (or --token-name <item>, or user_token_name in the config file)
run: bbapprove doctor`

// RemovedTokenFlagHelp explains --token, which no longer exists.
const RemovedTokenFlagHelp = `bbapprove: --token was removed (a token on the command line is visible to other users).
Set the BBAPPROVE_TOKEN environment variable, or name a 1Password item with --token-name NAME /
BBAPPROVE_TOKEN_NAME / user_token_name (see README, section Token); run: bbapprove doctor`

// IsRemovedTokenFlag reports whether arg is the removed --token option (--token-name is valid).
func IsRemovedTokenFlag(arg string) bool {
	return arg == "--token" || strings.HasPrefix(arg, "--token=")
}
