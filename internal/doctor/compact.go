package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bbapprove/internal/config"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
	"bbapprove/internal/style"
	"bbapprove/internal/update"
)

// The dense report: one line per area. A line marked warn or FAIL is always printed; only ok lines and
// explanations are cut. `bbapprove doctor -v` prints the full report instead (see Options.Verbose).

// compactLabelWidth is the width of the area label column (the longest fixed label, "disallow", plus a space; a longer one such as a forge type gets one space).
const compactLabelWidth = 9

// area prints one report line: a status gutter, the area label and the text.
func (r *report) area(kind, label, format string, a ...any) {
	r.line(kind, "%s%s", r.pad(style.Dim, label, max(compactLabelWidth, len([]rune(label))+1)), fmt.Sprintf(format, a...))
}

func (r *report) compact(ctx context.Context, cfg *config.Config, cfgErr error) {
	r.compactHeader()
	r.compactUpdate(ctx, cfg)
	r.compactConfig(cfg, cfgErr)
	if cfg != nil {
		r.compactAdapter(cfg)
	}
	r.compactToken(cfg)
	r.compactIgnore()
	if cfg != nil {
		r.compactDisallow(cfg)
	}
	r.compactEnvironment()
	r.compactTools(ctx)
}

func (r *report) compactHeader() {
	o := r.o
	v := o.Version
	if v == "" {
		v = "dev"
	}
	install := "release archive"
	if update.DetectInstall(o.InstallRoot) == update.Checkout {
		install = "git checkout"
	}
	fmt.Fprintf(r.w, "bbapprove %s %s %s %s\n", r.p(style.Version, v), r.p(style.Dim, fmt.Sprintf("(%s/%s, %s)", o.GOOS, o.GOARCH, o.GoVersion)), r.p(style.Dim, "·"), install)
	if o.Executable == nil {
		return
	}
	exe, err := o.Executable()
	if err != nil {
		r.area("warn", "binary", "unknown (%v)", err)
		return
	}
	if res, err := o.EvalLinks(exe); err == nil {
		exe = res
	}
	bin := "     " + r.pad(style.Dim, "binary", compactLabelWidth) + r.p(style.Path, exe)
	// the root is implied by the binary's location (<root>/bin/<binary>) unless it is somewhere else
	var root string
	if filepath.Dir(filepath.Dir(exe)) != filepath.Clean(o.InstallRoot) {
		root = " " + r.p(style.Dim, "(root ") + r.p(style.Path, o.InstallRoot) + r.p(style.Dim, ")")
	}
	onPath, err := o.LookPath("bbapprove")
	if err != nil {
		fmt.Fprintf(r.w, "%s%s %s\n", bin, root, r.p(style.Dim, "(not on PATH: run it by full path, or run bin/build --install)"))
		return
	}
	resolved := onPath
	if res, err := o.EvalLinks(onPath); err == nil {
		resolved = res
	}
	fmt.Fprintf(r.w, "%s%s %s%s%s\n", bin, root, r.p(style.Dim, "(on PATH: "), r.p(style.Path, onPath), r.p(style.Dim, ")"))
	if resolved != exe && filepath.Dir(resolved) != filepath.Dir(exe) {
		r.line("warn", "PATH shadowing: `bbapprove` on your PATH is %s, not this binary (%s)", r.p(style.Path, onPath), r.p(style.Path, exe))
	}
}

func (r *report) compactUpdate(ctx context.Context, cfg *config.Config) {
	if r.o.Offline {
		r.area("", "update", "%s", r.p(style.Dim, "skipped (")+r.p(style.Flag, "--offline")+r.p(style.Dim, ")"))
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
		r.area("warn", "update", "%s %v", r.p(style.Err, "could not check:"), err)
	case res.Outdated:
		r.area("warn", "update", "%s (run: bbapprove %s)", r.p(style.Warn, "update available: "+res.Latest.Tag), r.p(style.Flag, "--update"))
	default:
		r.area("ok", "update", "%s (%s)", r.p(style.OK, "up to date"), r.p(style.Version, res.Latest.Tag))
	}
}

func (r *report) compactConfig(cfg *config.Config, err error) {
	cands, winner, exists := r.lookup()
	switch {
	case err != nil:
		r.area("fail", "config", "%v", err)
		r.fail("config: "+err.Error(), "fix the file, or create a fresh one with: bbapprove --init --force")
		if winner >= 0 && winner <= 1 && !exists(cands[winner].path) { // an explicit file that is missing
			r.printLookup(cands, winner, exists)
		}
		return
	case cfg.File == "":
		r.area("fail", "config", "no config file found")
		r.printLookup(cands, winner, exists)
	default:
		via := [...]string{"--config flag", "BBAPPROVE_CONFIG", "repo root", "XDG config"}[winner]
		r.area("ok", "config", "%s %s, valid TOML", r.p(style.Path, cfg.File), r.p(style.Dim, "("+via+")"))
		// The file mode is only a note (the file holds no secrets), so -v shows it and this report does not.
		for _, w := range cfg.Warnings {
			r.line("warn", "%s", w)
		}
		for _, p := range config.Placeholders(cfg.File) {
			r.fail("config "+cfg.File+": "+p, "edit the file and set your own values")
		}
	}
	fmt.Fprintf(r.w, "     %s\n", r.p(style.Dim, "effective settings (source: flag, env, file or default):"))
	for _, l := range strings.Split(strings.TrimRight(cfg.SettingsCompactThemed(r.t), "\n"), "\n") {
		fmt.Fprintf(r.w, "        %s\n", l)
	}
	r.checkSettings(cfg)
}

// compactAdapter reports only a problem: a working adapter is part of the tool, and its type and path are in the settings list.
func (r *report) compactAdapter(cfg *config.Config) {
	path := cfg.Adapter.Value
	if !strings.Contains(path, "/") {
		if p, err := r.o.LookPath(path); err == nil {
			path = p
		}
	}
	st, err := os.Stat(path)
	switch {
	case err != nil:
		r.area("fail", cfg.Type.Value, "adapter %s does not exist", r.p(style.Path, path))
		r.fail("adapter "+path+" does not exist", "check type / BBAPPROVE_ADAPTER, or reinstall (bin/build --install)")
	case st.IsDir() || st.Mode().Perm()&0o111 == 0:
		r.area("fail", cfg.Type.Value, "adapter %s exists but is not executable", r.p(style.Path, path))
		r.fail("adapter "+path+" is not executable", "chmod +x "+path)
	}
}

func (r *report) compactToken(cfg *config.Config) {
	name, src := "", ""
	if cfg != nil {
		name, src = cfg.TokenName.Value, cfg.TokenName.Source
	}
	if r.o.env("BBAPPROVE_TOKEN") != "" {
		if name != "" {
			r.area("ok", "token", "%s set (1Password item %q [%s] is ignored)", r.p(style.Env, "BBAPPROVE_TOKEN"), name, src)
		} else {
			r.area("ok", "token", "%s set", r.p(style.Env, "BBAPPROVE_TOKEN"))
		}
		return
	}
	if name == "" {
		r.area("fail", "token", "no token: %s is not set and no 1Password item name is configured", r.p(style.Env, "BBAPPROVE_TOKEN"))
		r.fail("no API token source", "export BBAPPROVE_TOKEN=<token>, or set BBAPPROVE_TOKEN_NAME / --token-name / user_token_name to a 1Password item")
		return
	}
	r.area("ok", "token", "1Password item %q [%s] (not looked up by doctor)", name, src)
	if _, err := r.o.LookPath("op"); err != nil {
		r.area("warn", "", "op (1Password CLI) NOT found on PATH; the item cannot be looked up")
	}
}

func (r *report) compactIgnore() {
	path := r.o.IgnorePath
	if path == "" {
		r.area("fail", "ignore", "no home directory: cannot locate the ignore list")
		r.fail("ignore list location unknown", "set XDG_CONFIG_HOME or HOME")
		return
	}
	st, err := os.Stat(path)
	if err != nil && os.IsNotExist(err) {
		r.area("ok", "ignore", "%s, does not exist (nothing is ignored)", r.p(style.Path, path))
		return
	}
	entries, lerr := ignore.Load(path)
	if lerr != nil || err != nil {
		if lerr == nil {
			lerr = err
		}
		r.area("fail", "ignore", "%s exists but is unreadable: %v", r.p(style.Path, path), lerr)
		r.fail("ignore list "+path+" is unreadable (bbapprove fails closed and refuses to run)", "chmod 600 "+path)
		return
	}
	r.area("ok", "ignore", "%s, %s, %04o", r.p(style.Path, path), plural(len(entries), "entry", "entries"), st.Mode().Perm())
}

// compactDisallow prints one line, and only when the environment or the disallow file adds entries: the
// entries from the config file are already in the settings list.
func (r *report) compactDisallow(cfg *config.Config) {
	var parts []string
	if env := normalizeAll(strings.Split(r.o.env("BBAPPROVE_DISALLOW"), ",")); len(env) > 0 {
		parts = append(parts, r.p(style.Env, "env")+": "+strings.Join(env, ", "))
	}
	df := cfg.DisallowFile.Value
	if st, err := os.Stat(df); err == nil && !st.IsDir() {
		if entries := rules.LoadDisallow(df, ""); len(entries) > 0 {
			parts = append(parts, r.p(style.Dim, "file")+": "+strings.Join(entries, ", "))
		} else {
			parts = append(parts, r.p(style.Dim, "file")+": "+r.p(style.Path, df)+" (no entries)")
		}
	}
	if len(parts) > 0 {
		r.area("", "disallow", "%s", strings.Join(parts, "; "))
	}
}

// compactEnvironment prints one line naming the variables that change behaviour, or nothing when none is set.
func (r *report) compactEnvironment() {
	var names []string
	seen := map[string]bool{}
	for _, kv := range r.o.Environ {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "BBAPPROVE_") && !seen[k] {
			seen[k] = true
			names = append(names, k)
		} else if (k == "NO_COLOR" || k == "FORCE_COLOR") && v != "" && !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	var parts []string
	for _, k := range names {
		parts = append(parts, r.p(style.Env, k)+" "+r.p(style.Dim, "set"))
	}
	r.area("", "env", "%s", strings.Join(parts, ", "))
}

func (r *report) compactTools(ctx context.Context) {
	var parts []string
	for _, t := range []string{"git", "gh", "op"} {
		if _, err := r.o.LookPath(t); err != nil {
			note := ""
			if t == "gh" {
				note = " (needed for --outdated / --update)"
			}
			parts = append(parts, r.p(style.Dim, t+" not found"+note))
			continue
		}
		part := t
		if t == "gh" {
			cctx, cancel := context.WithTimeout(ctx, ghAuthTimeout)
			_, err := r.o.Runner.Run(cctx, update.Cmd{Name: "gh", Args: []string{"auth", "status"}})
			cancel()
			if err != nil {
				part += r.p(style.Dim, " (not logged in)")
			} else {
				part += r.p(style.Dim, " (logged in)")
			}
		}
		parts = append(parts, part)
	}
	r.area("", "tools", "%s", strings.Join(parts, ", "))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
