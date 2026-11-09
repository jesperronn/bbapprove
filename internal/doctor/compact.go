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

// The report: one line per area, in both modes. A line marked warn or FAIL is always printed and carries
// its fix hint; only ok lines and explanations are cut from the default one. `bbapprove doctor -v` adds
// the root, the lookup order, the file mode, every setting, tool paths and the environment, but never
// prints a fact twice (see Options.Verbose).

// compactLabelWidth is the width of the area label column (the longest fixed label, "disallow", plus a space; a longer one such as a forge type gets one space).
const compactLabelWidth = 9

// area prints one report line: a status gutter, the area label and the text.
func (r *report) area(kind, label, format string, a ...any) {
	r.line(kind, "%s%s", r.pad(style.Dim, label, max(compactLabelWidth, len([]rune(label))+1)), fmt.Sprintf(format, a...))
}

// problem prints a FAIL line whose text carries the fix, and counts it for the closing line.
func (r *report) problem(label, what, fix string) {
	r.area("fail", label, "%s", what+" — "+fix)
	r.problems++
}

func (r *report) body(ctx context.Context, cfg *config.Config, cfgErr error) {
	r.compactHeader()
	r.compactUpdate(ctx, cfg)
	r.compactConfig(cfg, cfgErr)
	if cfg != nil {
		r.compactAdapter(cfg)
	}
	r.compactToken(cfg)
	r.compactIgnore()
	if r.o.Verbose {
		r.verboseEnvironment(cfg)
	} else {
		if cfg != nil {
			r.compactDisallow(cfg)
		}
		r.compactEnvironment()
	}
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
	if o.Verbose {
		r.area("", "root", "%s", r.p(style.Path, o.InstallRoot))
	}
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
	// the root is implied by the binary's location (<root>/bin/<binary>) unless it is somewhere else; -v has its own root line
	if !o.Verbose && filepath.Dir(filepath.Dir(exe)) != filepath.Clean(o.InstallRoot) {
		bin += " " + r.p(style.Dim, "(root ") + r.p(style.Path, o.InstallRoot) + r.p(style.Dim, ")")
	}
	onPath, err := o.LookPath("bbapprove")
	if err != nil {
		fmt.Fprintf(r.w, "%s %s\n", bin, r.p(style.Dim, "(not on PATH: run it by full path, or run bin/build --install)"))
		return
	}
	resolved := onPath
	if res, err := o.EvalLinks(onPath); err == nil {
		resolved = res
	}
	if resolved != exe && filepath.Dir(resolved) != filepath.Dir(exe) {
		fmt.Fprintln(r.w, bin)
		r.area("warn", "PATH", "shadowed: %s is not this binary", r.p(style.Path, onPath))
		return
	}
	fmt.Fprintf(r.w, "%s %s%s%s\n", bin, r.p(style.Dim, "(on PATH: "), r.p(style.Path, onPath), r.p(style.Dim, ")"))
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
		r.problem("config", err.Error(), "fix the file, or create a fresh one with: bbapprove --init --force")
		if r.o.Verbose || winner >= 0 && winner <= 1 && !exists(cands[winner].path) { // -v always; else an explicit file that is missing
			r.printLookup(cands, winner, exists)
		}
		return
	case cfg.File == "":
		if cfg.Repository.Value != "" && cfg.AuthorEmail.Value != "" {
			r.area("warn", "config", "no config file: all settings come from the environment")
		} else {
			r.problem("config", "no config file found", "run: bbapprove --init   (or: cp "+config.TemplateName+" "+config.ConfigName+" in "+r.p(style.Path, r.o.InstallRoot)+" and edit it)")
		}
		r.printLookup(cands, winner, exists)
	default:
		text := r.p(style.Path, cfg.File) + " " + r.p(style.Dim, "("+[...]string{"--config flag", "BBAPPROVE_CONFIG", "repo root", "XDG config"}[winner]+")") + ", valid TOML"
		if st, err := os.Stat(cfg.File); r.o.Verbose && err == nil {
			mode := st.Mode().Perm()
			text += fmt.Sprintf(", mode %04o", mode)
			if mode&0o077 != 0 {
				text += r.p(style.Dim, " (default 0600; no secrets, a note only)")
			}
		}
		if ph := config.Placeholders(cfg.File); len(ph) > 0 {
			r.problem("config", text+", "+strings.Join(ph, "; "), "edit the file and set your own values")
		} else {
			r.area("ok", "config", "%s", text)
		}
		for _, w := range cfg.Warnings {
			r.line("warn", "%s", w)
		}
		if r.o.Verbose {
			r.printLookup(cands, winner, exists)
		}
	}
	fmt.Fprintf(r.w, "     %s\n", r.p(style.Dim, "effective settings (source: flag, env, file or default):"))
	settings := cfg.SettingsCompactThemed(r.t)
	if r.o.Verbose {
		settings = cfg.SettingsWithRows(r.t, r.settingsRows(cfg))
	}
	for _, l := range strings.Split(strings.TrimRight(settings, "\n"), "\n") {
		fmt.Fprintf(r.w, "        %s\n", l)
	}
	r.checkSettings(cfg)
}

// printLookup prints the lookup order, one short line per candidate: its state, and its path unless it is
// the file in use (the config line already names that).
func (r *report) printLookup(cands []candidate, winner int, exists func(string) bool) {
	for i, c := range cands {
		label := r.pad(labelClass(c.label), c.label, 17)
		state, class, path := "not found", style.Dim, c.path
		switch {
		case c.path == "" && i < 2:
			state, path = "not set", ""
		case c.path == "":
			state = "no home directory"
		case exists(c.path) && i == winner:
			state, class, path = "used", style.OK, ""
		case exists(c.path):
			state = "shadowed"
		}
		if path != "" {
			path = "  " + r.p(style.Path, path)
		}
		fmt.Fprintf(r.w, "        %d. %s %s%s\n", i+1, label, r.p(class, state), path)
	}
}

// settingsRows describes the two settings rows that have several origins, for -v: the disallow entries with where
// they come from, and a disallow file that does not exist.
func (r *report) settingsRows(cfg *config.Config) map[string]string {
	rows := map[string]string{}
	var tags []string
	if r.o.DisallowFlag != "" {
		tags = append(tags, "flag")
	} else if len(normalizeAll(strings.Split(r.o.env("BBAPPROVE_DISALLOW"), ","))) > 0 {
		tags = append(tags, "env")
	}
	df := cfg.DisallowFile.Value
	if st, err := os.Stat(df); err == nil && !st.IsDir() {
		if len(rules.LoadDisallow(df, "")) > 0 {
			tags = append(tags, "disallow_file")
		}
	} else {
		rows["disallow_file"] = df + "  " + r.t.Sources(cfg.DisallowFile.Source, "not found")
	}
	if len(cfg.FileDisallow) > 0 {
		tags = append(tags, "file")
	}
	if len(cfg.Disallow) == 0 {
		rows["disallow"] = r.p(style.Dim, "(none)")
	} else {
		rows["disallow"] = strings.Join(cfg.Disallow, ", ")
		if len(tags) > 0 {
			rows["disallow"] += "  " + r.t.Sources(tags...)
		}
	}
	return rows
}

// checkSettings reports the settings a working config file must have: repository and author_email.
func (r *report) checkSettings(cfg *config.Config) {
	if cfg.File == "" {
		return // the config line already says so
	}
	file := r.p(style.Path, cfg.File)
	if cfg.Repository.Value == "" {
		r.problem("settings", "repository is not set", "set repository in "+file+" or BBAPPROVE_REPOSITORY")
	}
	if cfg.AuthorEmail.Value == "" {
		r.problem("settings", "author_email is not set", "set author_email in "+file+" or BBAPPROVE_AUTHOR_EMAIL")
	}
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
		r.problem(cfg.Type.Value, "adapter "+r.p(style.Path, path)+" does not exist", "check type / BBAPPROVE_ADAPTER, or reinstall (bin/build --install)")
	case st.IsDir() || st.Mode().Perm()&0o111 == 0:
		r.problem(cfg.Type.Value, "adapter "+r.p(style.Path, path)+" exists but is not executable", "chmod +x "+r.p(style.Path, path))
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
		r.problem("token", "no token: "+r.p(style.Env, "BBAPPROVE_TOKEN")+" is not set and no 1Password item name is configured",
			"export BBAPPROVE_TOKEN=<token>, or set BBAPPROVE_TOKEN_NAME / --token-name / user_token_name to a 1Password item")
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
		r.problem("ignore", "no home directory: cannot locate the ignore list", "set XDG_CONFIG_HOME or HOME")
		return
	}
	st, err := os.Stat(path)
	if err != nil && os.IsNotExist(err) {
		r.area("ok", "ignore", "%s, does not exist (nothing is ignored)", r.p(style.Path, path))
		r.ignoreNote()
		return
	}
	entries, lerr := ignore.Load(path)
	if lerr != nil || err != nil {
		if lerr == nil {
			lerr = err
		}
		r.problem("ignore", r.p(style.Path, path)+" exists but is unreadable ("+lerr.Error()+"); bbapprove fails closed and refuses to run", "chmod 600 "+r.p(style.Path, path))
		return
	}
	r.area("ok", "ignore", "%s, %s, %04o", r.p(style.Path, path), plural(len(entries), "entry", "entries"), st.Mode().Perm())
	r.ignoreNote()
}

// ignoreNote is the one-sentence explanation of the ignore list, printed with -v only.
func (r *report) ignoreNote() {
	if r.o.Verbose {
		fmt.Fprintf(r.w, "     %s\n", r.p(style.Dim, "rows matching these entries are shown as IGNORED and never acted on; edit the file or press i on an ignored row in the TUI to remove an entry"))
	}
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

// verboseEnvironment prints one line with every variable that is set and changes behaviour, with its value. Variables the report
// already shows (a setting whose source is env, the config file chosen by BBAPPROVE_CONFIG, the token) are left out.
func (r *report) verboseEnvironment(cfg *config.Config) {
	shown := map[string]bool{"BBAPPROVE_TOKEN": true} // the token line says it is set; its value is never printed
	if cfg != nil {
		for name, v := range map[string]config.Value{
			"BBAPPROVE_ADAPTER": cfg.Adapter, "BBAPPROVE_REPOSITORY": cfg.Repository, "BBAPPROVE_TOKEN_NAME": cfg.TokenName,
			"BBAPPROVE_AUTHOR_EMAIL": cfg.AuthorEmail, "BBAPPROVE_UPDATE_REPO": cfg.UpdateRepo,
			"BBAPPROVE_DELETE_SOURCE_BRANCH": cfg.DeleteSourceBranch, "BBAPPROVE_DISALLOW_FILE": cfg.DisallowFile,
		} {
			shown[name] = v.Source == config.FromEnv
		}
		shown["BBAPPROVE_DISALLOW"] = r.o.DisallowFlag == "" && len(normalizeAll(strings.Split(r.o.env("BBAPPROVE_DISALLOW"), ","))) > 0
		if _, winner, _ := r.lookup(); cfg.File != "" && winner == 1 {
			shown["BBAPPROVE_CONFIG"] = true
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, kv := range r.o.Environ {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "BBAPPROVE_") && !seen[k] && !shown[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	sort.Strings(names)
	names = append(names, "NO_COLOR", "FORCE_COLOR", "XDG_CONFIG_HOME", "EDITOR", "VISUAL")
	var parts []string
	for _, k := range names {
		if v := r.o.env(k); v != "" {
			parts = append(parts, r.p(style.Env, k)+"="+v)
		}
	}
	if len(parts) > 0 {
		r.area("", "env", "%s", strings.Join(parts, ", "))
	}
}

func (r *report) compactTools(ctx context.Context) {
	var parts []string
	sep := ", "
	if r.o.Verbose {
		sep = " · "
	}
	for _, t := range []string{"git", "gh", "op"} {
		p, err := r.o.LookPath(t)
		if err != nil {
			note := ""
			if t == "gh" {
				note = " (needed for --outdated / --update)"
			}
			parts = append(parts, r.p(style.Dim, t+" not found"+note))
			continue
		}
		part := t
		if r.o.Verbose {
			part += " " + r.p(style.Path, p)
		}
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
	r.area("", "tools", "%s", strings.Join(parts, r.p(style.Dim, sep)))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
