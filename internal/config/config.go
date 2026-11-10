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
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"bbapprove/internal/rules"
	"bbapprove/internal/style"
	"bbapprove/internal/userconfig"
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
	// DefaultDeleteSourceBranch is on: merged bot branches would otherwise pile up on the forge.
	DefaultDeleteSourceBranch = "true"
	// TemplateName is the committed example config; the real file is TemplateName's sibling ConfigName.
	TemplateName = "bbapprove.config.template"
	ConfigName   = "bbapprove.config.toml"
)

// knownKeys are the keys a config file may contain; anything else only draws a warning.
var knownKeys = []string{"type", "repository", "user_token_name", "author_email", "disallow", "update_repo", "delete_source_branch"}

// fileData is the TOML file's shape.
type fileData struct {
	Type          string   `toml:"type"`
	Repository    string   `toml:"repository"`
	UserTokenName string   `toml:"user_token_name"`
	AuthorEmail   string   `toml:"author_email"`
	Disallow      []string `toml:"disallow"`
	UpdateRepo    string   `toml:"update_repo"`
	// DeleteSourceBranch is a pointer so an absent key (default true) differs from an explicit false.
	DeleteSourceBranch *bool `toml:"delete_source_branch"`
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
	GOOS    string              // OS whose user-config location to search; empty means the running OS

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
	BuiltIn                                  bool  // the type is served in-process (see UsesBuiltIn); Adapter is then not run
	DeleteSourceBranch                       Value // "true" or "false": delete the PR's source branch after a merge
	Disallow                                 []string
	FileDisallow                             []string // the `disallow` entries from the config file alone
	DisallowFile                             Value

	File        string   // config file in use ("" when none)
	DefaultPath string   // where first-run setup would create it
	Searched    []string // paths looked at (for the first-run message)
	Warnings    []string // e.g. unknown keys; the caller prints them to stderr
}

// UsesBuiltIn reports whether forge type typ is served by the adapter built into the binary: only
// "bitbucket", and only while BBAPPROVE_ADAPTER (adapterEnv) is not set. Any other type, and an
// explicit adapter, run an executable.
func UsesBuiltIn(typ, adapterEnv string) bool { return typ == "bitbucket" && adapterEnv == "" }

// RootOfExecutable is the install root for the binary at exe: the parent of the directory holding it
// when that directory is named bin (<root>/bin/bbapprove-go, the git checkout and Unix archive layout),
// else the directory itself (<root>/bbapprove.exe, the Windows archive layout).
func RootOfExecutable(exe string) string {
	dir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(dir), "bin") {
		return filepath.Dir(dir)
	}
	return dir
}

// ConfigHome returns the directory that holds bbapprove's per-user files: $XDG_CONFIG_HOME (as seen
// through env, falling back to the process) on any OS, else %AppData% on Windows, else ~/.config.
func ConfigHome(env func(string) string) string { return userconfig.Home(env) }

// ConfigHomeOn is ConfigHome as goos sees it (empty means the running OS); doctor uses it to report on the OS it is told.
func ConfigHomeOn(goos string, env func(string) string) string { return userconfig.HomeOn(goos, env) }

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
		if h := ConfigHomeOn(in.GOOS, in.Env); h != "" {
			cands = append(cands, filepath.Join(h, userconfig.Dir, "config.toml"))
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

	dsb, err := resolveDeleteSourceBranch(in.Env("BBAPPROVE_DELETE_SOURCE_BRANCH"), data.DeleteSourceBranch)
	if err != nil {
		return nil, err
	}
	c.DeleteSourceBranch = dsb

	if t := c.Type.Value; t == "." || t == ".." || strings.ContainsAny(t, `/\`) {
		return nil, fmt.Errorf("invalid type %q: it must be a plain adapter name (adapters/<type>)", t)
	}
	c.BuiltIn = UsesBuiltIn(c.Type.Value, in.Env("BBAPPROVE_ADAPTER"))
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

// ParseBool accepts true/false/1/0/yes/no, case-insensitively.
func ParseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	}
	return false, false
}

// resolveDeleteSourceBranch applies env > file > default; an invalid env value is an error.
func resolveDeleteSourceBranch(env string, file *bool) (Value, error) {
	if env != "" {
		b, ok := ParseBool(env)
		if !ok {
			return Value{}, fmt.Errorf("invalid BBAPPROVE_DELETE_SOURCE_BRANCH %q: use true, false, 1, 0, yes or no", env)
		}
		return Value{strconv.FormatBool(b), FromEnv}, nil
	}
	if file != nil {
		return Value{strconv.FormatBool(*file), FromFile}, nil
	}
	return Value{DefaultDeleteSourceBranch, FromDefault}, nil
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
func (c *Config) Missing() error { return c.MissingFor(style.Theme{}) }

// MissingFor is Missing with the message painted by t (stripped of colour it is identical).
func (c *Config) MissingFor(t style.Theme) error {
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
		searched := make([]string, len(c.Searched))
		for i, p := range c.Searched {
			searched[i] = t.Paint(style.Path, p)
		}
		msg := t.Sprintf("{fail:bbapprove:} \"{val:%s}\" is not set (%s).\n"+
			"Set it in a config file (or via {env:%s}): {cmd:cp %s %s} and edit it.\n",
			s.name, where, s.env, TemplateName, ConfigName) +
			"Searched: " + strings.Join(searched, ", ")
		if c.File == "" {
			if def := c.DefaultPath; def != "" {
				msg += "\nOr create it in the default location (kept across upgrades):\n" + createHint(t, def)
			}
		}
		return errors.New(strings.TrimRight(msg, "\n"))
	}
	return nil
}

// ChildEnv returns environ with the resolved settings the adapter needs appended (BBAPPROVE_BASE_URL,
// BBAPPROVE_TOKEN_NAME, BBAPPROVE_DELETE_SOURCE_BRANCH); later entries win. The adapter never reads TOML.
func (c *Config) ChildEnv(environ []string) []string {
	return append(append([]string{}, environ...),
		"BBAPPROVE_BASE_URL="+c.Repository.Value,
		"BBAPPROVE_TOKEN_NAME="+c.TokenName.Value,
		"BBAPPROVE_DELETE_SOURCE_BRANCH="+c.DeleteSourceBranch.Value)
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
func (c *Config) SettingsThemed(t style.Theme) string { return c.settingsThemed(t, false, nil) }

// SettingsCompactThemed is SettingsThemed with the disallow list on one line as "N entries: a, b"
// ("(none)" when empty), as `bbapprove doctor` shows it.
func (c *Config) SettingsCompactThemed(t style.Theme) string { return c.settingsThemed(t, true, nil) }

// SettingsWithRows is SettingsThemed with the text after the label replaced for the named rows (for
// example "disallow"), for a caller that knows more about a row than its Value does.
func (c *Config) SettingsWithRows(t style.Theme, rows map[string]string) string {
	return c.settingsThemed(t, false, rows)
}

func (c *Config) settingsThemed(t style.Theme, compactDisallow bool, over map[string]string) string {
	var b strings.Builder
	label := func(name string) string {
		l := name + ":"
		return t.Paint(style.Dim, l) + strings.Repeat(" ", max(22-len(l), 0)) + " "
	}
	row := func(name string, v Value) {
		val := v.Value
		if val == "" {
			val = "(not set)"
		}
		if o, ok := over[name]; ok {
			fmt.Fprintf(&b, "%s%s\n", label(name), o)
			return
		}
		fmt.Fprintf(&b, "%s%s  %s\n", label(name), val, t.Source(v.Source))
	}
	row("type", c.Type)
	if c.BuiltIn {
		row("adapter", Value{"built-in (" + c.Type.Value + ")", c.Adapter.Source})
	} else {
		row("adapter", c.Adapter)
	}
	row("repository", c.Repository)
	row("user_token_name", c.TokenName)
	row("author_email", c.AuthorEmail)
	row("update_repo", c.UpdateRepo)
	row("delete_source_branch", c.DeleteSourceBranch)
	row("disallow_file", c.DisallowFile)
	list := strings.Join(c.Disallow, ", ")
	if compactDisallow {
		switch n := len(c.Disallow); n {
		case 0:
			list = t.Paint(style.Dim, "(none)")
		case 1:
			list = "1 entry: " + list
		default:
			list = fmt.Sprintf("%d entries: %s", n, list)
		}
	}
	if o, ok := over["disallow"]; ok {
		list = o
	}
	fmt.Fprintf(&b, "%s%s\n", label("disallow"), list)
	return b.String()
}

const tokenMissingMarkup = `{fail:bbapprove: no API token.}
Set {env:BBAPPROVE_TOKEN} to a Bitbucket HTTP access token:
  {cmd:export BBAPPROVE_TOKEN=...}
or name a 1Password item holding it (looked up with "op", field token, then credential):
  {cmd:export BBAPPROVE_TOKEN_NAME=}{val:<item>}     (or {flag:--token-name} {val:<item>}, or {val:user_token_name} in the config file)
run: bbapprove {cmd:doctor}`

const removedTokenFlagMarkup = `{fail:bbapprove: --token was removed} (a token on the command line is visible to other users).
Set the {env:BBAPPROVE_TOKEN} environment variable, or name a 1Password item with {flag:--token-name} {val:NAME} /
{env:BBAPPROVE_TOKEN_NAME} / {val:user_token_name} (see: bbapprove {cmd:help} token); run: bbapprove {cmd:doctor}`

// TokenMissingHelp is printed when neither BBAPPROVE_TOKEN nor a 1Password item name is set.
var TokenMissingHelp = TokenMissingHelpFor(style.Theme{})

// RemovedTokenFlagHelp explains --token, which no longer exists.
var RemovedTokenFlagHelp = RemovedTokenFlagHelpFor(style.Theme{})

// TokenMissingHelpFor is TokenMissingHelp painted by t.
func TokenMissingHelpFor(t style.Theme) string { return t.MustRender(tokenMissingMarkup) }

// RemovedTokenFlagHelpFor is RemovedTokenFlagHelp painted by t.
func RemovedTokenFlagHelpFor(t style.Theme) string { return t.MustRender(removedTokenFlagMarkup) }

// IsRemovedTokenFlag reports whether arg is the removed --token option (--token-name is valid).
func IsRemovedTokenFlag(arg string) bool {
	return arg == "--token" || strings.HasPrefix(arg, "--token=")
}
