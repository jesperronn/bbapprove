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
	"strings"
)

// Template is the example config embedded in the binary, so first-run setup works from a release
// archive and from anywhere. The canonical file is the committed repo-root bbapprove.config.template;
// go:embed cannot reach outside this package directory, so internal/config/bbapprove.config.template
// is a byte-identical copy. TestEmbeddedTemplateMatchesRoot fails when they drift: after editing the
// root file run `cp bbapprove.config.template internal/config/`.
//
//go:embed bbapprove.config.template
var Template string

// DefaultPath is where first-run setup creates the config: $XDG_CONFIG_HOME/bbapprove/config.toml
// (else ~/.config/bbapprove/config.toml), outside the install tree so upgrades never lose it.
func DefaultPath(env func(string) string) string {
	h := ConfigHome(env)
	if h == "" {
		return ""
	}
	return filepath.Join(h, "bbapprove", "config.toml")
}

// ErrExists is returned by WriteTemplate when the file is already there and force is false.
var ErrExists = errors.New("file already exists")

// WriteTemplate writes the embedded template to path (directory 0700, file 0600). It never replaces an
// existing file unless force is set.
func WriteTemplate(path string, force bool) error {
	if path == "" {
		return errors.New("cannot determine a config location (set HOME or XDG_CONFIG_HOME, or pass --config FILE)")
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
	def := DefaultPath(in.Env)
	if !f.interactive() || def == "" {
		fmt.Fprintln(out, cfg.Missing())
		return cfg, 1, false
	}
	fmt.Fprintln(out, "bbapprove: no config file found.")
	fmt.Fprintf(out, "Create %s from the template and open it in your editor? [Y/n] ", def)
	var line string
	var rerr error
	if f.In != nil {
		line, rerr = bufio.NewReader(f.In).ReadString('\n')
	}
	// EOF with nothing typed (stdin closed) is not consent; a bare Enter is.
	if a := strings.ToLower(strings.TrimSpace(line)); (rerr != nil && a == "") || (a != "" && a != "y" && a != "yes") {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Not creating it. To do it yourself:")
		fmt.Fprint(out, createHint(def))
		return cfg, 1, false
	}
	if err := WriteTemplate(def, false); err != nil {
		fmt.Fprintf(out, "bbapprove: cannot create %s: %v\n", def, err)
		return cfg, 1, false
	}
	fmt.Fprintf(out, "Created %s (mode 0600).\n", def)
	opened, err := f.runEditor(ctx, in.Env, def)
	switch {
	case err != nil:
		fmt.Fprintf(out, "bbapprove: editor failed: %v\nEdit %s, then run the same command again.\n", err, def)
		return cfg, 1, false
	case !opened:
		fmt.Fprintf(out, "No $VISUAL or $EDITOR is set. Edit %s, then run the same command again.\n", def)
		return cfg, 1, false
	}
	newCfg, err := Load(in)
	if err == nil {
		err = newCfg.Missing()
	}
	if err != nil {
		fmt.Fprintf(out, "%v\nconfig still not valid: edit %s and run the same command again.\n", err, def)
		return cfg, 1, false
	}
	fmt.Fprintln(out, "config OK")
	return newCfg, 0, true
}

// createHint is the manual two-line instruction for creating the config at path.
func createHint(path string) string {
	return fmt.Sprintf("  mkdir -p %s && cp bbapprove.config.template %s\n  (or: bbapprove --init), then edit it.\n",
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
	if path == "" {
		path = DefaultPath(env)
	}
	if err := WriteTemplate(path, force); err != nil {
		if errors.Is(err, ErrExists) {
			fmt.Fprintf(out, "bbapprove: %s already exists; not overwriting (use --init --force to replace it).\n", path)
		} else {
			fmt.Fprintf(out, "bbapprove: --init: %v\n", err)
		}
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %s (mode 0600).\n", path)
	if f.interactive() {
		opened, err := f.runEditor(ctx, env, path)
		if err != nil {
			fmt.Fprintf(out, "bbapprove: editor failed: %v\n", err)
			return 1
		}
		if !opened {
			fmt.Fprintf(stdout, "No $VISUAL or $EDITOR is set; edit %s yourself.\n", path)
		}
	} else {
		fmt.Fprintf(stdout, "Edit %s to set repository and author_email.\n", path)
	}
	fmt.Fprintln(stdout, "Next: bbapprove --print-config   (shows the effective settings and where each came from)")
	return 0
}
