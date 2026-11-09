// Package doctor implements `bbapprove doctor`: a read-only report on the installation, the config files
// in use, the ignore and disallow lists, the token sources and the external tools. It needs no forge
// access and no token, never prints a secret and never runs `op` (it only looks for it on PATH).
//
// Everything that touches the process (environment, PATH, running executable, external commands) is
// injected through Options so tests are hermetic. The real wiring lives in internal/app.
package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bbapprove/internal/config"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
	"bbapprove/internal/update"
)

// maxListed caps how many ignore / disallow entries are printed.
const maxListed = 50

// ghAuthTimeout bounds `gh auth status`.
const ghAuthTimeout = 5 * time.Second

// Options carries everything doctor reads from the process.
type Options struct {
	Environ []string // KEY=VALUE, like os.Environ()
	Stdout  io.Writer
	Stderr  io.Writer
	IsTTY   bool // stdout is a terminal: colour and symbols are allowed unless NO_COLOR is set

	Runner      update.Runner                // gh / git; nil means the real one
	LookPath    func(string) (string, error) // exec.LookPath seam
	Executable  func() (string, error)       // os.Executable seam
	EvalLinks   func(string) (string, error) // filepath.EvalSymlinks seam; nil means the real one
	Getwd       func() (string, error)       // reserved seam (currently only shown for context)
	HomeDir     func() (string, error)       // os.UserHomeDir seam
	Version     string
	GOOS        string
	GOARCH      string
	GoVersion   string
	InstallRoot string // parent of the directory holding the binary

	ConfigFlag    string // --config
	TokenNameFlag string // --token-name
	DisallowFlag  string // --disallow
	IgnorePath    string // the ignore list the app will read
	Offline       bool   // skip the update check
}

func (o Options) env(key string) string {
	v := ""
	for _, kv := range o.Environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val // last wins
		}
	}
	return v
}

func (o Options) color() bool {
	if o.env("NO_COLOR") != "" {
		return false
	}
	return o.IsTTY || o.env("FORCE_COLOR") != ""
}

type report struct {
	o        Options
	w        io.Writer
	color    bool
	problems []string
}

const (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	bold   = "\033[1m"
)

// mark returns a 4-column status gutter: ok, FAIL or warn (symbols with colour), or blanks.
func (r *report) mark(kind string) string {
	if r.color {
		switch kind {
		case "ok":
			return green + "✓" + reset + "    "
		case "fail":
			return red + "✗" + reset + "    "
		case "warn":
			return yellow + "!" + reset + "    "
		}
		return "     "
	}
	switch kind {
	case "ok":
		return "ok   "
	case "fail":
		return "FAIL "
	case "warn":
		return "warn "
	}
	return "     "
}

func (r *report) head(s string) {
	if r.color {
		s = bold + s + reset
	}
	fmt.Fprintf(r.w, "\n%s\n", s)
}

// line prints an indented line with a status gutter.
func (r *report) line(kind, format string, a ...any) {
	fmt.Fprintf(r.w, "%s%s\n", r.mark(kind), fmt.Sprintf(format, a...))
}

func (r *report) kv(kind, label, format string, a ...any) {
	r.line(kind, "%-13s %s", label+":", fmt.Sprintf(format, a...))
}

func (r *report) fail(what, fix string) { r.problems = append(r.problems, what+" — "+fix) }

// Run prints the report and returns the exit code: 0 when nothing FAILed (warnings are fine), 1 otherwise.
func Run(ctx context.Context, o Options) int {
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.LookPath == nil {
		o.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	}
	if o.Runner == nil {
		o.Runner = update.ExecRunner{}
	}
	if o.EvalLinks == nil {
		o.EvalLinks = filepath.EvalSymlinks
	}
	r := &report{o: o, w: o.Stdout, color: o.color()}

	r.sectionVersion()
	cfg, cfgErr := config.Load(config.Input{
		Env: o.env, RepoDir: o.InstallRoot, ConfigFlag: o.ConfigFlag, TokenNameFlag: o.TokenNameFlag, DisallowFlag: o.DisallowFlag,
	})
	if cfgErr != nil {
		cfg = nil
	}
	r.sectionUpdate(ctx, cfg)
	r.sectionConfigFiles(cfg, cfgErr)
	r.sectionSettings(cfg, cfgErr)
	r.sectionAdapter(cfg)
	r.sectionToken(cfg)
	r.sectionIgnore()
	r.sectionDisallow(cfg)
	r.sectionEnvironment()
	r.sectionTools(ctx)

	fmt.Fprintln(r.w)
	for _, p := range r.problems {
		if r.color {
			fmt.Fprintf(r.w, "%sFAIL%s %s\n", red, reset, p)
		} else {
			fmt.Fprintf(r.w, "FAIL %s\n", p)
		}
	}
	if len(r.problems) == 0 {
		fmt.Fprintln(r.w, "all good")
		return 0
	}
	fmt.Fprintf(r.w, "%d problem(s) found\n", len(r.problems))
	return 1
}

func (r *report) sectionVersion() {
	o := r.o
	v := o.Version
	if v == "" {
		v = "dev"
	}
	fmt.Fprintf(r.w, "bbapprove %s  (%s/%s, %s)\n", v, o.GOOS, o.GOARCH, o.GoVersion)
	install := "release archive"
	if update.DetectInstall(o.InstallRoot) == update.Checkout {
		install = "git checkout"
	}
	r.kv("", "install", "%s", install)
	r.kv("", "root", "%s", o.InstallRoot)
	if o.Executable == nil {
		return
	}
	exe, err := o.Executable()
	if err != nil {
		r.kv("warn", "binary", "unknown (%v)", err)
		return
	}
	if res, err := o.EvalLinks(exe); err == nil {
		exe = res
	}
	r.kv("", "binary", "%s", exe)
	onPath, err := o.LookPath("bbapprove")
	if err != nil {
		r.kv("", "on PATH", "bbapprove not found on PATH (run it by full path, or run bin/build --install)")
		return
	}
	resolved := onPath
	if res, err := o.EvalLinks(onPath); err == nil {
		resolved = res
	}
	if resolved == exe || filepath.Dir(resolved) == filepath.Dir(exe) {
		r.kv("", "on PATH", "%s", onPath)
		return
	}
	r.kv("warn", "on PATH", "%s", onPath)
	r.line("warn", "PATH shadowing: `bbapprove` on your PATH is %s, not this binary (%s)", onPath, exe)
}

func (r *report) sectionUpdate(ctx context.Context, cfg *config.Config) {
	r.head("update:")
	if r.o.Offline {
		r.line("", "skipped (--offline)")
		return
	}
	slug := r.o.env("BBAPPROVE_UPDATE_REPO")
	if slug == "" {
		slug = config.DefaultUpdateRepo
		if cfg != nil {
			slug = cfg.UpdateRepo.Value
		}
	}
	res, err := update.Check(ctx, update.Options{
		Runner: r.o.Runner, InstallRoot: r.o.InstallRoot, Version: r.o.Version, Slug: slug,
		PathEnv: r.o.env("PATH"), GOOS: r.o.GOOS, GOARCH: r.o.GOARCH,
	})
	switch {
	case err != nil:
		r.line("warn", "could not check: %v", err)
	case res.Outdated:
		r.line("warn", "update available: %s (run: bbapprove --update)", res.Latest.Tag)
	default:
		r.line("ok", "up to date (latest release: %s)", res.Latest.Tag)
	}
}

// candidate is one place a config file may live.
type candidate struct {
	label, path string
}

func (r *report) sectionConfigFiles(cfg *config.Config, err error) {
	o := r.o
	r.head("config:")
	env := o.env
	var repoPath, xdgPath string
	repoPath = filepath.Join(o.InstallRoot, config.ConfigName)
	if h := config.ConfigHome(env); h != "" {
		xdgPath = filepath.Join(h, "bbapprove", "config.toml")
	}
	cands := []candidate{
		{"--config flag", o.ConfigFlag},
		{"BBAPPROVE_CONFIG", env("BBAPPROVE_CONFIG")},
		{"repo root", repoPath},
		{"XDG config", xdgPath},
	}
	exists := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	// Winner, mirroring config.Load: an explicit file (flag, else env) always wins; else the first existing one.
	winner := -1
	switch {
	case cands[0].path != "":
		winner = 0
	case cands[1].path != "":
		winner = 1
	default:
		for i := 2; i < len(cands); i++ {
			if cands[i].path != "" && exists(cands[i].path) {
				winner = i
				break
			}
		}
	}
	fmt.Fprintf(r.w, "     lookup order (an explicit file wins, else the first one found):\n")
	for i, c := range cands {
		if c.path == "" {
			if i < 2 {
				fmt.Fprintf(r.w, "        %d. %-17s (not set)\n", i+1, c.label)
			} else {
				fmt.Fprintf(r.w, "        %d. %-17s (no home directory)\n", i+1, c.label)
			}
			continue
		}
		state := "not found"
		switch {
		case exists(c.path) && i == winner:
			state = "found, used"
		case exists(c.path) && winner >= 0:
			state = "found, shadowed by " + cands[winner].path
		case exists(c.path):
			state = "found"
		case i == winner:
			state = "NOT FOUND, but selected explicitly"
		}
		fmt.Fprintf(r.w, "        %d. %-17s %s  [%s]\n", i+1, c.label, c.path, state)
	}

	if err != nil {
		r.kv("fail", "in use", "%v", err)
		r.fail("config: "+err.Error(), "fix the file, or create a fresh one with: bbapprove --init --force")
		return
	}
	if cfg.File == "" {
		r.kv("fail", "in use", "none")
		return
	}
	via := [...]string{"--config flag", "BBAPPROVE_CONFIG", "repo root", "XDG config"}[winner]
	r.kv("ok", "in use", "%s (via %s)", cfg.File, via)
	if st, err := os.Stat(cfg.File); err == nil {
		mode := st.Mode().Perm()
		if mode&0o077 != 0 {
			r.kv("warn", "file mode", "%04o: readable by group/other (default is 0600; the file holds no secrets, so this is only a note)", mode)
		} else {
			r.kv("", "file mode", "%04o", mode)
		}
	}
	r.kv("ok", "parse", "valid TOML")
	for _, w := range cfg.Warnings {
		r.line("warn", "%s", w)
	}
	for _, p := range config.Placeholders(cfg.File) {
		r.fail("config "+cfg.File+": "+p, "edit the file and set your own values")
	}
}

func (r *report) sectionSettings(cfg *config.Config, cfgErr error) {
	if cfg == nil {
		r.line("", "effective settings: unavailable (config error above)")
		return
	}
	fmt.Fprintf(r.w, "     effective settings (source: flag, env, file or default):\n")
	for _, l := range strings.Split(strings.TrimRight(cfg.Settings(), "\n"), "\n") {
		fmt.Fprintf(r.w, "        %s\n", l)
	}
	if cfg.File == "" {
		if cfg.Repository.Value == "" || cfg.AuthorEmail.Value == "" {
			r.fail("no config file found (searched "+strings.Join(cfg.Searched, ", ")+") and repository/author_email are not both set",
				"run: bbapprove --init   (or: cp "+config.TemplateName+" "+config.ConfigName+" in "+r.o.InstallRoot+" and edit it)")
		} else {
			r.line("warn", "no config file: all settings come from the environment")
		}
		return
	}
	if cfg.Repository.Value == "" {
		r.fail("repository is not set", "set repository in "+cfg.File+" or BBAPPROVE_REPOSITORY")
	}
	if cfg.AuthorEmail.Value == "" {
		r.fail("author_email is not set", "set author_email in "+cfg.File+" or BBAPPROVE_AUTHOR_EMAIL")
	}
}

func (r *report) sectionAdapter(cfg *config.Config) {
	r.head("adapter:")
	if cfg == nil {
		r.line("", "unavailable (config error above)")
		return
	}
	r.kv("", "type", "%s", cfg.Type.Value)
	path := cfg.Adapter.Value
	if !strings.Contains(path, "/") {
		if p, err := r.o.LookPath(path); err == nil {
			path = p
		}
	}
	r.kv("", "path", "%s  [%s]", path, cfg.Adapter.Source)
	st, err := os.Stat(path)
	switch {
	case err != nil:
		r.kv("fail", "state", "does not exist")
		r.fail("adapter "+path+" does not exist", "check type / BBAPPROVE_ADAPTER, or reinstall (bin/build --install)")
	case st.IsDir() || st.Mode().Perm()&0o111 == 0:
		r.kv("fail", "state", "exists but is not executable")
		r.fail("adapter "+path+" is not executable", "chmod +x "+path)
	default:
		r.kv("ok", "state", "exists, executable")
	}
}

func (r *report) sectionToken(cfg *config.Config) {
	r.head("token (never printed; in order of precedence):")
	r.line("", "%-22s %s", "1. --token flag", "per run only; not visible to doctor")
	envState := "not set"
	if r.o.env("BBAPPROVE_TOKEN") != "" {
		envState = "set"
	}
	r.line("", "%-22s %s", "2. BBAPPROVE_TOKEN env", envState)
	name := config.DefaultTokenName
	if cfg != nil {
		name = cfg.TokenName.Value
	}
	opState := "op: NOT found on PATH"
	have := false
	if p, err := r.o.LookPath("op"); err == nil {
		opState = "op: found at " + p
		have = true
	}
	r.line("", "%-22s item %q via adapter (%s)", "3. 1Password", name, opState)
	if envState == "not set" && !have {
		r.line("warn", "no token source here: pass --token, set BBAPPROVE_TOKEN or install the 1Password CLI")
	}
}

func (r *report) sectionIgnore() {
	r.head("ignore list:")
	path := r.o.IgnorePath
	if path == "" {
		r.line("fail", "no home directory: cannot locate the ignore list")
		r.fail("ignore list location unknown", "set XDG_CONFIG_HOME or HOME")
		return
	}
	r.kv("", "path", "%s", path)
	st, err := os.Stat(path)
	if err != nil && os.IsNotExist(err) {
		r.kv("ok", "state", "does not exist: no dependency is ignored (default location; created by pressing i on a row in the TUI)")
		return
	}
	entries, lerr := ignore.Load(path)
	if lerr != nil || err != nil {
		if lerr == nil {
			lerr = err
		}
		r.kv("fail", "state", "exists but is unreadable: %v", lerr)
		r.fail("ignore list "+path+" is unreadable (bbapprove fails closed and refuses to run)", "chmod 600 "+path)
		return
	}
	r.kv("ok", "state", "readable")
	r.kv("", "mode", "%04o", st.Mode().Perm())
	r.kv("", "entries", "%d", len(entries))
	for i, e := range entries {
		if i == maxListed {
			r.line("", "  ... and %d more", len(entries)-maxListed)
			break
		}
		r.line("", "  %s", e)
	}
	r.line("", "rows matching these entries are shown as IGNORED and never acted on; edit the file or press i on an ignored row in the TUI to remove an entry")
}

func (r *report) listEntries(label string, items []string) {
	if len(items) == 0 {
		r.kv("", label, "(none)")
		return
	}
	r.kv("", label, "%d", len(items))
	for i, e := range items {
		if i == maxListed {
			r.line("", "  ... and %d more", len(items)-maxListed)
			break
		}
		r.line("", "  %s", e)
	}
}

func normalizeAll(parts []string) []string {
	var out []string
	for _, p := range parts {
		if n := rules.Normalize(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (r *report) sectionDisallow(cfg *config.Config) {
	r.head("disallow (matching PRs are SKIPped):")
	if cfg == nil {
		r.line("", "unavailable (config error above)")
	} else {
		r.listEntries("config file", cfg.FileDisallow)
	}
	r.listEntries("BBAPPROVE_DISALLOW", normalizeAll(strings.Split(r.o.env("BBAPPROVE_DISALLOW"), ",")))
	if cfg != nil {
		df := cfg.DisallowFile.Value
		state := "not found"
		if st, err := os.Stat(df); err == nil && !st.IsDir() {
			state = "found"
		}
		r.kv("", "file", "%s  [%s, %s]", df, cfg.DisallowFile.Source, state)
		if state == "found" {
			r.listEntries("file entries", rules.LoadDisallow(df, ""))
		}
	}
	r.kv("", "--disallow", "adds more entries per run (flag only)")
}

func (r *report) sectionEnvironment() {
	r.head("environment:")
	var names []string
	seen := map[string]bool{}
	for _, kv := range r.o.Environ {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "BBAPPROVE_") && !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		r.line("", "no BBAPPROVE_* variables set")
	}
	for _, k := range names {
		if k == "BBAPPROVE_TOKEN" {
			r.line("", "%s=set (value hidden)", k)
		} else {
			r.line("", "%s=%s", k, r.o.env(k))
		}
	}
	for _, k := range []string{"NO_COLOR", "FORCE_COLOR", "XDG_CONFIG_HOME", "EDITOR", "VISUAL"} {
		if v := r.o.env(k); v != "" {
			r.line("", "%s=%s", k, v)
		} else {
			r.line("", "%s not set", k)
		}
	}
}

func (r *report) sectionTools(ctx context.Context) {
	r.head("tools:")
	for _, t := range []string{"git", "gh", "op"} {
		p, err := r.o.LookPath(t)
		if err != nil {
			note := ""
			if t == "gh" {
				note = " (needed for --outdated / --update)"
			}
			r.kv("", t, "not found on PATH%s", note)
			continue
		}
		r.kv("", t, "%s", p)
		if t == "gh" {
			cctx, cancel := context.WithTimeout(ctx, ghAuthTimeout)
			_, err := r.o.Runner.Run(cctx, update.Cmd{Name: "gh", Args: []string{"auth", "status"}})
			cancel()
			if err != nil {
				r.kv("", "gh auth", "not logged in")
			} else {
				r.kv("", "gh auth", "logged in")
			}
		}
	}
}
