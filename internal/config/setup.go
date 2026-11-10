package config

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"bbapprove/internal/style"
	"bbapprove/internal/userconfig"
)

// Template is the example config embedded in the binary, so first-run setup works from a release
// archive and from anywhere. The canonical file is the committed repo-root bbapprove.config.template;
// go:embed cannot reach outside this package directory, so internal/config/bbapprove.config.template
// is a byte-identical copy. TestEmbeddedTemplateMatchesRoot fails when they drift: after editing the
// root file run `cp bbapprove.config.template internal/config/`.
//
//go:embed bbapprove.config.template
var Template string

// DefaultPath is where first-run setup creates the config: $XDG_CONFIG_HOME/bbapprove/config.toml,
// else %AppData%\bbapprove\config.toml on Windows, else ~/.config/bbapprove/config.toml; outside the
// install tree so upgrades never lose it.
func DefaultPath(env func(string) string) string { return userconfig.Path(env, "config.toml") }

// ErrExists is returned by WriteTemplate when the file is already there and force is false.
var ErrExists = errors.New("file already exists")

// WriteTemplate writes the embedded template to path (directory 0700, file 0600). It never replaces an
// existing file unless force is set.
func WriteTemplate(path string, force bool) error {
	if path == "" {
		return errors.New("cannot determine a config location (set HOME or XDG_CONFIG_HOME, or APPDATA on Windows, or pass --config FILE)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s: %w", path, ErrExists)
		}
		return err
	}
	if _, err := io.WriteString(f, Template); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // force may have reused a file with looser permissions
}

// SplitWords splits an $EDITOR value into words like a minimal shell: whitespace separates, single and
// double quotes group, backslash escapes outside single quotes. No expansion, so nothing is executed.
func SplitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	in, quote := false, rune(0)
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc, in = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 || esc {
		return nil, fmt.Errorf("unterminated quote or escape in %q", s)
	}
	if in {
		words = append(words, cur.String())
	}
	return words, nil
}

// EditorCommand returns the words of $VISUAL (else $EDITOR), or nil when neither is set.
func EditorCommand(env func(string) string) ([]string, error) {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(env(k)); v != "" {
			w, err := SplitWords(v)
			if err != nil {
				return nil, fmt.Errorf("$%s: %v", k, err)
			}
			if len(w) > 0 {
				return w, nil
			}
		}
	}
	return nil, nil
}

// maxEditRounds bounds how often first-run setup re-opens the editor for unchanged placeholders.
const maxEditRounds = 3

// Placeholders lists what is still the template's example content in the file at path: the whole file
// when byte-identical to the template, else repository / author_email still at their example value.
// Used by first-run setup only; ordinary config validation accepts example values.
func Placeholders(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if string(b) == Template {
		return []string{"the file is unchanged from the template"}
	}
	var ex, got fileData
	if toml.Unmarshal([]byte(Template), &ex) != nil || toml.Unmarshal(b, &got) != nil {
		return nil
	}
	var out []string
	if got.Repository == ex.Repository {
		out = append(out, "repository is still the example value "+ex.Repository)
	}
	if got.AuthorEmail == ex.AuthorEmail {
		out = append(out, "author_email is still the example value "+ex.AuthorEmail)
	}
	return out
}

// EditorRunner opens path in the editor given as argv words and waits for it to exit.
type EditorRunner func(ctx context.Context, editor []string, path string) error

// ExecEditor is the real EditorRunner: exec (no shell) with the terminal attached.
func ExecEditor(ctx context.Context, editor []string, path string) error {
	cmd := exec.CommandContext(ctx, editor[0], append(append([]string{}, editor[1:]...), path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// FirstRun carries the injectable pieces of the interactive setup; nil fields mean "not interactive"
// (IsTTY), os-backed defaults (Editor) or no input (In).
type FirstRun struct {
	IsTTY  func() bool // stdin and stderr are both terminals
	In     io.Reader
	Out    io.Writer // prompts and messages (stderr)
	Editor EditorRunner
	// ErrTheme paints what goes to Out (stderr), OutTheme what Init writes to stdout. The zero value is
	// plain text; each stream gets its own theme because only one of them may be a terminal.
	ErrTheme, OutTheme style.Theme
}

func (f FirstRun) interactive() bool { return f.IsTTY != nil && f.IsTTY() }

func (f FirstRun) runEditor(ctx context.Context, env func(string) string, path string) (opened bool, err error) {
	words, err := EditorCommand(env)
	if err != nil {
		return false, err
	}
	if words == nil {
		return false, nil
	}
	run := f.Editor
	if run == nil {
		run = ExecEditor
	}
	return true, run(ctx, words, path)
}

// Setup is the first-run flow, called when Load found no config file and Missing reports a gap. It
// returns the config to continue with and cont=true, or code (the exit status) and cont=false.
func Setup(ctx context.Context, in Input, cfg *Config, f FirstRun) (_ *Config, code int, cont bool) {
	out := f.Out
	if out == nil {
		out = io.Discard
	}
	t := f.ErrTheme
	def := DefaultPath(in.Env)
	if !f.interactive() || def == "" {
		fmt.Fprintln(out, cfg.MissingFor(t))
		return cfg, 1, false
	}
	fmt.Fprintln(out, t.Sprintf("{warn:bbapprove: no config file found.}"))
	fmt.Fprint(out, t.Sprintf("Create {path:%s} from the template and open it in your editor? {dim:[Y/n]} ", def))
	var rd *bufio.Reader
	if f.In != nil {
		rd = bufio.NewReader(f.In)
	}
	if !askYes(rd) {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Not creating it. To do it yourself:")
		fmt.Fprint(out, createHint(t, def))
		return cfg, 1, false
	}
	if err := WriteTemplate(def, false); err != nil {
		fmt.Fprintln(out, t.Sprintf("{fail:bbapprove:} cannot create {path:%s}: %s", def, err))
		return cfg, 1, false
	}
	fmt.Fprintln(out, t.Sprintf("{ok:Created} {path:%s} (mode 0600).", def))
	for round := 1; ; round++ {
		opened, err := f.runEditor(ctx, in.Env, def)
		switch {
		case err != nil:
			fmt.Fprintln(out, t.Sprintf("{fail:bbapprove:} editor failed: %s\nEdit {path:%s}, then run the same command again.", err, def))
			return cfg, 1, false
		case !opened:
			fmt.Fprintln(out, t.Sprintf("No {env:$VISUAL} or {env:$EDITOR} is set. Edit {path:%s}, then run the same command again.", def))
			return cfg, 1, false
		}
		newCfg, err := Load(in)
		if err == nil {
			err = newCfg.MissingFor(t)
		}
		if err != nil {
			fmt.Fprintln(out, t.Sprintf("%s\n{fail:config still not valid:} edit {path:%s} and run the same command again.", err, def))
			return cfg, 1, false
		}
		ph := Placeholders(def)
		if len(ph) == 0 {
			fmt.Fprintln(out, t.Sprintf("{ok:config OK}"))
			return newCfg, 0, true
		}
		for _, p := range ph {
			fmt.Fprintln(out, t.Sprintf("{warn:bbapprove:} %s", p))
		}
		if round >= maxEditRounds {
			fmt.Fprintln(out, t.Sprintf("Giving up after %s rounds; {path:%s} was left in place. Edit it, or run: bbapprove {flag:--init} {flag:--force}", round, def))
			return cfg, 1, false
		}
		fmt.Fprint(out, t.Sprintf("Edit again? {dim:[Y/n]} "))
		if !askYes(rd) {
			fmt.Fprintln(out, t.Sprintf("\n{path:%s} was left in place. Edit it, or run: bbapprove {flag:--init} {flag:--force}", def))
			return cfg, 1, false
		}
	}
}

// askYes reads one line: Enter, y or yes is consent. EOF with nothing typed (stdin closed) is not.
func askYes(rd *bufio.Reader) bool {
	if rd == nil {
		return false
	}
	line, err := rd.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	if err != nil && a == "" {
		return false
	}
	return a == "" || a == "y" || a == "yes"
}

// createHint is the manual two-line instruction for creating the config at path, painted by t.
func createHint(t style.Theme, path string) string { return createHintFor(runtime.GOOS, t, path) }

// createHintFor is createHint for goos: cmd.exe and PowerShell both take mkdir and copy, not mkdir -p and cp.
func createHintFor(goos string, t style.Theme, path string) string {
	if goos == "windows" {
		return t.Sprintf("  {cmd:mkdir} {path:\"%s\"} && {cmd:copy} bbapprove.config.template {path:\"%s\"}\n  (or: bbapprove {flag:--init}), then edit it.\n",
			path[:max(strings.LastIndexAny(path, `/\`), 0)], path)
	}
	return t.Sprintf("  {cmd:mkdir -p} {path:%s} && {cmd:cp} bbapprove.config.template {path:%s}\n  (or: bbapprove {flag:--init}), then edit it.\n",
		shellQuote(filepath.Dir(path)), shellQuote(path))
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:=@%+,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Init implements `bbapprove --init`: write the template to path (default location when empty),
// refusing to overwrite unless force, open the editor when interactive, and say what to do next.
func Init(ctx context.Context, env func(string) string, path string, force bool, f FirstRun, stdout io.Writer) int {
	out := f.Out
	if out == nil {
		out = io.Discard
	}
	et, ot := f.ErrTheme, f.OutTheme
	if path == "" {
		path = env("BBAPPROVE_CONFIG") // flag > env > default
	}
	if path == "" {
		path = DefaultPath(env)
	}
	if err := WriteTemplate(path, force); err != nil {
		if errors.Is(err, ErrExists) {
			fmt.Fprintln(out, et.Sprintf("{fail:bbapprove:} {path:%s} already exists; not overwriting (use {flag:--init} {flag:--force} to replace it).", path))
		} else {
			fmt.Fprintln(out, et.Sprintf("{fail:bbapprove:} {flag:--init}: %s", err))
		}
		return 1
	}
	fmt.Fprintln(stdout, ot.Sprintf("{ok:Wrote} {path:%s} (mode 0600).", path))
	if f.interactive() {
		opened, err := f.runEditor(ctx, env, path)
		if err != nil {
			fmt.Fprintln(out, et.Sprintf("{fail:bbapprove:} editor failed: %s", err))
			return 1
		}
		if !opened {
			fmt.Fprintln(stdout, ot.Sprintf("No {env:$VISUAL} or {env:$EDITOR} is set; edit {path:%s} yourself.", path))
		}
		for _, p := range Placeholders(path) {
			fmt.Fprintln(stdout, ot.Sprintf("{warn:warning:} %s", p))
		}
	} else {
		fmt.Fprintln(stdout, ot.Sprintf("Edit {path:%s} to set {val:repository} and {val:author_email}.", path))
	}
	fmt.Fprintln(stdout, ot.Sprintf("{h:Next:} bbapprove {flag:--print-config}   {dim:(shows the effective settings and where each came from)}"))
	return 0
}
