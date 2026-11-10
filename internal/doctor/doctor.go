// Package doctor implements `bbapprove doctor`: a read-only report on the installation, the config files
// in use, the ignore and disallow lists, the token source and the external tools. It needs no forge
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
	"strings"
	"time"

	"bbapprove/internal/config"
	"bbapprove/internal/rules"
	"bbapprove/internal/style"
	"bbapprove/internal/update"
)

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
	Verbose       bool   // print the full report (every ok line and explanation), not the dense one
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
	return style.Enabled(o.env, o.IsTTY)
}

type report struct {
	o        Options
	w        io.Writer
	color    bool
	t        style.Theme
	problems int // how many FAIL lines were printed
}

// p paints text with a style class (identity without colour).
func (r *report) p(class, text string) string { return r.t.Paint(class, text) }

// mark returns a 5-column status gutter: ok, FAIL or warn (symbols with colour), or blanks.
func (r *report) mark(kind string) string {
	if r.color {
		switch kind {
		case "ok":
			return r.p(style.OK, "✓") + "    "
		case "fail":
			return r.p(style.Fail, "✗") + "    "
		case "warn":
			return r.p(style.Warn, "!") + "    "
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

// line prints an indented line with a status gutter.
func (r *report) line(kind, format string, a ...any) {
	fmt.Fprintf(r.w, "%s%s\n", r.mark(kind), fmt.Sprintf(format, a...))
}

// pad returns label painted with class, then spaces up to width columns (padding stays unpainted).
func (r *report) pad(class, label string, width int) string {
	return r.p(class, label) + strings.Repeat(" ", max(width-len([]rune(label)), 0))
}

// labelClass picks the class of a label: env variable names and flags keep their usual colours.
func labelClass(label string) string {
	switch {
	case strings.HasPrefix(label, "BBAPPROVE_"):
		return style.Env
	case strings.HasPrefix(label, "--") && !strings.Contains(label, " "):
		return style.Flag
	}
	return style.Dim
}

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
	r.t = style.New(r.color)

	cfg, cfgErr := config.Load(config.Input{
		Env: o.env, RepoDir: o.InstallRoot, GOOS: o.GOOS, ConfigFlag: o.ConfigFlag, TokenNameFlag: o.TokenNameFlag, DisallowFlag: o.DisallowFlag,
	})
	if cfgErr != nil {
		cfg = nil
	}
	r.body(ctx, cfg, cfgErr)

	fmt.Fprintln(r.w)
	if r.problems == 0 {
		fmt.Fprintln(r.w, r.p(style.Good, "all good"))
		return 0
	}
	fmt.Fprintln(r.w, r.p(style.Bad, fmt.Sprintf("%d problem(s) found", r.problems)))
	return 1
}

// candidate is one place a config file may live.
type candidate struct {
	label, path string
}

// lookup lists the four places a config file may live and which one wins, mirroring config.Load: an
// explicit file (flag, else env) always wins; else the first existing one.
func (r *report) lookup() (cands []candidate, winner int, exists func(string) bool) {
	o := r.o
	env := o.env
	repoPath := filepath.Join(o.InstallRoot, config.ConfigName)
	var userPath string
	if h := config.ConfigHomeOn(o.GOOS, env); h != "" {
		userPath = filepath.Join(h, "bbapprove", "config.toml")
	}
	userLabel := "XDG config"
	if o.GOOS == "windows" && env("XDG_CONFIG_HOME") == "" {
		userLabel = "AppData config" // %AppData%\bbapprove\config.toml
	}
	cands = []candidate{
		{"--config flag", o.ConfigFlag},
		{"BBAPPROVE_CONFIG", env("BBAPPROVE_CONFIG")},
		{"repo root", repoPath},
		{userLabel, userPath},
	}
	exists = func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	winner = -1
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
	return cands, winner, exists
}

// unixModes reports whether file permission bits mean anything on the reported OS: Windows files have
// none, so the mode note and the executable check are skipped there.
func (o Options) unixModes() bool { return o.GOOS != "windows" }

func normalizeAll(parts []string) []string {
	var out []string
	for _, p := range parts {
		if n := rules.Normalize(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}
