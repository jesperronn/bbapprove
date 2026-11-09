package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"bbapprove/internal/testfix"
	"bbapprove/internal/update"
)

// pathCase is one human-readable output path of the CLI: a --outdated / --update / --init run, an error,
// a first-run prompt. build creates a fresh hermetic fixture (temp dirs, fake gh/git, in-process editor)
// and returns the options to run. The plain output of every case is pinned by a golden file
// (TestGoldenPlainPaths) and the coloured output is checked against it (TestEveryPathIsColoured).
type pathCase struct {
	name   string
	covers []string // what the case exercises: "--flag", "help:TOPIC" or "error:NAME"; see TestEveryCommandHasAColourCase
	build  func(t *testing.T, fx *fixture) Options
}

// fixture holds the temp dirs of one run; their paths are replaced by placeholders in the output.
type fixture struct {
	repo, xdg, home, base string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fx := &fixture{base: t.TempDir()}
	fx.repo = filepath.Join(fx.base, "repo")
	fx.xdg = filepath.Join(fx.base, "xdg")
	fx.home = filepath.Join(fx.base, "home")
	for _, d := range []string{fx.repo, fx.xdg, fx.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", fx.home)
	t.Setenv("XDG_CONFIG_HOME", fx.xdg)
	return fx
}

// normalise replaces the fixture's directories with stable names.
func (fx *fixture) normalise(s string) string {
	s = strings.ReplaceAll(s, "/private/var/", "/var/")
	return strings.NewReplacer(fx.repo, "$REPO", fx.xdg, "$XDG", fx.home, "$HOME", fx.base, "$BASE").Replace(s)
}

func (fx *fixture) env(extra ...string) []string {
	return append([]string{"XDG_CONFIG_HOME=" + fx.xdg, "HOME=" + fx.home, "PATH=" + filepath.Join(fx.base, "nobin")}, extra...)
}

// result is one finished run.
type result struct {
	code        int
	out, errOut string
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\n--- stdout ---\n%s--- stderr ---\n%s", r.code, r.out, r.errOut)
}

// runPathCase builds and runs c with extra environment (NO_COLOR / FORCE_COLOR) appended.
func runPathCase(t *testing.T, c pathCase, extraEnv ...string) result {
	t.Helper()
	return runPathCaseWith(t, c, nil, extraEnv...)
}

// runPathCaseWith is runPathCase with a hook to change the options (for example the TTY flags).
func runPathCaseWith(t *testing.T, c pathCase, mod func(*Options), extraEnv ...string) result {
	t.Helper()
	fx := newFixture(t)
	o := c.build(t, fx)
	if mod != nil {
		mod(&o)
	}
	var so, se bytes.Buffer
	o.Stdout, o.Stderr = &so, &se
	o.Environ = append(append([]string{}, o.Environ...), extraEnv...)
	if o.Version == "" {
		o.Version = "v1.0.0-rc.2"
	}
	code := Run(context.Background(), o)
	return result{code, fx.normalise(so.String()), fx.normalise(se.String())}
}

// scriptRunner answers gh and git calls from a function; nothing is executed.
type scriptRunner func(c update.Cmd) ([]byte, error)

func (s scriptRunner) Run(_ context.Context, c update.Cmd) ([]byte, error) { return s(c) }

const ghRC3 = `[{"tagName":"v1.0.0-rc.3","isPrerelease":true,"isDraft":false,"publishedAt":"2026-01-01T00:00:00Z"}]`

// ghOnly answers `gh release list` with list and fails on anything else.
func ghOnly(list string) scriptRunner {
	return func(c update.Cmd) ([]byte, error) {
		if c.Name == "gh" && len(c.Args) > 1 && c.Args[1] == "list" {
			return []byte(list), nil
		}
		return nil, fmt.Errorf("unexpected %s %v", c.Name, c.Args)
	}
}

// checkoutRunner fakes a clean checkout on branch that can be updated; streamed commands print a line.
func checkoutRunner(status, branch string) scriptRunner {
	return func(c update.Cmd) ([]byte, error) {
		a := strings.Join(c.Args, " ")
		switch {
		case c.Name == "gh" && strings.HasPrefix(a, "release list"):
			return []byte(ghRC3), nil
		case c.Name == "git" && strings.HasPrefix(a, "status"):
			return []byte(status), nil
		case c.Name == "git" && strings.HasPrefix(a, "rev-parse"):
			return []byte(branch + "\n"), nil
		case c.Name == "git" && strings.HasPrefix(a, "merge-base"):
			return nil, nil
		case strings.HasSuffix(c.Name, "bbapprove"):
			return []byte("bbapprove v1.0.0-rc.3\n"), nil
		}
		if c.Stdout != nil {
			fmt.Fprintf(c.Stdout, "[%s %s]\n", filepath.Base(c.Name), a)
		}
		return nil, nil
	}
}

func tarGz(t *testing.T, top string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, typ byte, mode int64, body string) {
		h := &tar.Header{Name: name, Typeflag: typ, Mode: mode, Size: int64(len(body))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	add(top+"/", tar.TypeDir, 0o755, "")
	add(top+"/bin/", tar.TypeDir, 0o755, "")
	add(top+"/bin/bbapprove", tar.TypeReg, 0o755, "#!/bin/sh\necho new\n")
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// archiveRunner fakes gh for an archive update: release list/view/download (writing a real tarball and
// matching SHA256SUMS into the -D directory).
func archiveRunner(t *testing.T) scriptRunner {
	asset := "bbapprove-v1.0.0-rc.3-linux-amd64.tar.gz"
	data := tarGz(t, strings.TrimSuffix(asset, ".tar.gz"))
	sum := sha256.Sum256(data)
	return func(c update.Cmd) ([]byte, error) {
		a := strings.Join(c.Args, " ")
		switch {
		case strings.HasPrefix(a, "release list"):
			return []byte(ghRC3), nil
		case strings.HasPrefix(a, "release view"):
			return []byte(`{"body":"First note\nSecond note","url":"https://example.com/releases/tag/v1.0.0-rc.3"}`), nil
		case strings.HasPrefix(a, "release download"):
			dir := ""
			for i, x := range c.Args {
				if x == "-D" {
					dir = c.Args[i+1]
				}
			}
			os.WriteFile(filepath.Join(dir, asset), data, 0o644)
			os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  "+asset+"\n"), 0o644)
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %s %s", c.Name, a)
	}
}

// updateOpts is the base for the update cases: an install root (checkout or release archive tree) under fx.
func updateOpts(t *testing.T, fx *fixture, checkout bool, r scriptRunner, args ...string) Options {
	root := filepath.Join(fx.base, "bbapprove-v1.0.0-rc.2-linux-amd64")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if checkout {
		os.Mkdir(filepath.Join(root, ".git"), 0o755)
	}
	return Options{Args: args, Environ: fx.env("BBAPPROVE_UPDATE_REPO=example/repo"), RepoDir: root,
		UpdateRunner: r, GOOS: "linux", GOARCH: "amd64"}
}

func templateEditor(ctx context.Context, editor []string, path string) error { return nil }

func validEditor(ctx context.Context, editor []string, path string) error {
	return os.WriteFile(path, []byte(validCfg), 0o600)
}

func failingEditor(ctx context.Context, editor []string, path string) error {
	return errors.New("exit status 1")
}

func tty(yes bool) func() bool { return func() bool { return yes } }

// pathCases lists every human-readable output path that is not help, doctor or --print-config.
var pathCases = []pathCase{
	// --outdated
	{"outdated-behind-checkout", []string{"--outdated"}, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, ghOnly(ghRC3), "--outdated")
	}},
	{"outdated-behind-archive", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, false, ghOnly(ghRC3), "--outdated")
	}},
	{"outdated-up-to-date", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, ghOnly(ghRC3), "--outdated")
		o.Version = "v1.0.0-rc.3"
		return o
	}},
	{"outdated-dev-build", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, ghOnly(ghRC3), "--outdated")
		o.Version = "v1.0.0-rc.2-4-gabc1234-dirty"
		return o
	}},
	{"outdated-dev-build-ahead", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, ghOnly(ghRC3), "--outdated")
		o.Version = "v1.0.0-rc.3-2-gabc1234"
		return o
	}},
	{"outdated-pre", []string{"--pre"}, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, ghOnly(`[{"tagName":"v1.0.0","isPrerelease":false,"isDraft":false},{"tagName":"v1.1.0-rc.1","isPrerelease":true,"isDraft":false}]`), "--outdated", "--pre")
		o.Version = "v1.0.0"
		return o
	}},
	{"outdated-unknown-version", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, ghOnly(ghRC3), "--outdated", "--pre")
		o.Version = "dev"
		return o
	}},
	{"outdated-cannot-check", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, false, func(c update.Cmd) ([]byte, error) { return nil, errors.New("gh: not logged in") }, "--outdated")
	}},
	{"outdated-no-releases", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, false, ghOnly("[]"), "--outdated")
	}},
	// --update
	{"update-checkout-success", []string{"--update"}, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, checkoutRunner("", "main"), "--update", "--yes")
	}},
	{"update-checkout-dirty", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, checkoutRunner(" M README.md\n", "main"), "--update", "--yes")
	}},
	{"update-checkout-wrong-branch", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, checkoutRunner("", "feature"), "--update", "--yes")
	}},
	{"update-not-a-terminal", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, checkoutRunner("", "main"), "--update")
	}},
	{"update-prompt-declined", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, true, checkoutRunner("", "main"), "--update")
		o.IsTTY, o.Interactive, o.Stdin = true, tty(true), strings.NewReader("n\n")
		return o
	}},
	{"update-archive-success-symlink", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, archiveRunner(t), "--update", "--yes")
		os.WriteFile(filepath.Join(o.RepoDir, "bin", "bbapprove"), []byte("old"), 0o755)
		os.WriteFile(filepath.Join(o.RepoDir, "bbapprove.config.toml"), []byte("x = 1\n"), 0o600)
		bindir := filepath.Join(fx.base, "linkbin")
		os.MkdirAll(bindir, 0o755)
		if err := os.Symlink(filepath.Join(o.RepoDir, "bin", "bbapprove"), filepath.Join(bindir, "bbapprove")); err != nil {
			t.Fatal(err)
		}
		o.Environ = fx.env("BBAPPROVE_UPDATE_REPO=example/repo", "PATH="+bindir)
		return o
	}},
	{"update-archive-success-no-symlink", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, false, archiveRunner(t), "--update", "--yes")
	}},
	{"update-archive-gh-missing", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, false, func(c update.Cmd) ([]byte, error) {
			return nil, &exec.Error{Name: "gh", Err: exec.ErrNotFound}
		}, "--update", "--yes")
	}},
	{"update-archive-prompt-declined", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, archiveRunner(t), "--update")
		o.IsTTY, o.Interactive, o.Stdin = true, tty(true), strings.NewReader("\n")
		return o
	}},
	{"update-already-current", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, false, ghOnly(ghRC3), "--update", "--yes")
		o.Version = "v1.0.0-rc.3"
		return o
	}},
	{"update-cannot-check", nil, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, func(c update.Cmd) ([]byte, error) { return nil, errors.New("offline") }, "--update", "--yes")
	}},
	{"update-conflicts-outdated-update", []string{"--outdated"}, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, ghOnly(ghRC3), "--outdated", "--update")
	}},
	{"update-conflicts-apply", []string{"--apply"}, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, ghOnly(ghRC3), "--update", "--apply")
	}},
	{"update-yes-needs-update", []string{"--yes"}, func(t *testing.T, fx *fixture) Options {
		return updateOpts(t, fx, true, ghOnly(ghRC3), "--outdated", "--yes")
	}},
	{"update-bad-config-warning", nil, func(t *testing.T, fx *fixture) Options {
		o := updateOpts(t, fx, true, ghOnly(ghRC3), "--outdated")
		os.WriteFile(filepath.Join(o.RepoDir, "bbapprove.config.toml"), []byte("not = [valid\n"), 0o600)
		o.Environ = fx.env()
		return o
	}},
	// --init
	{"init-created", []string{"--init"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--init"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"init-exists", nil, func(t *testing.T, fx *fixture) Options {
		os.MkdirAll(filepath.Join(fx.xdg, "bbapprove"), 0o700)
		os.WriteFile(filepath.Join(fx.xdg, "bbapprove", "config.toml"), []byte("x = 1\n"), 0o600)
		return Options{Args: []string{"--init"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"init-force", []string{"--init"}, func(t *testing.T, fx *fixture) Options {
		os.MkdirAll(filepath.Join(fx.xdg, "bbapprove"), 0o700)
		os.WriteFile(filepath.Join(fx.xdg, "bbapprove", "config.toml"), []byte("x = 1\n"), 0o600)
		return Options{Args: []string{"--init", "--force"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"init-interactive-placeholders", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--init"}, Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo,
			Interactive: tty(true), Editor: templateEditor}
	}},
	{"init-interactive-no-editor", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--init"}, Environ: fx.env(), RepoDir: fx.repo, Interactive: tty(true)}
	}},
	{"init-editor-failed", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--init"}, Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo,
			Interactive: tty(true), Editor: failingEditor}
	}},
	// first run, token, config errors
	{"firstrun-not-interactive", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"firstrun-declined", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env(), RepoDir: fx.repo, Interactive: tty(true), Stdin: strings.NewReader("n\n")}
	}},
	{"firstrun-no-editor", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env(), RepoDir: fx.repo, Interactive: tty(true), Stdin: strings.NewReader("\n")}
	}},
	{"firstrun-editor-failed", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo, Interactive: tty(true),
			Stdin: strings.NewReader("y\n"), Editor: failingEditor}
	}},
	{"firstrun-still-invalid", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo, Interactive: tty(true),
			Stdin: strings.NewReader("\n"), Editor: func(ctx context.Context, e []string, p string) error {
				return os.WriteFile(p, []byte("repository = \"\"\n"), 0o600)
			}}
	}},
	{"firstrun-placeholders-edit-again", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo, Interactive: tty(true),
			Stdin: strings.NewReader("y\nn\n"), Editor: templateEditor}
	}},
	{"firstrun-giving-up", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo, Interactive: tty(true),
			Stdin: strings.NewReader("y\ny\ny\n"), Editor: templateEditor}
	}},
	{"firstrun-ok-then-token-missing", []string{"error:token-missing"}, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("EDITOR=fake-editor"), RepoDir: fx.repo, Interactive: tty(true),
			Stdin: strings.NewReader("\n"), Editor: validEditor}
	}},
	{"token-missing", []string{"error:token-missing"}, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("BBAPPROVE_REPOSITORY=https://bb.example.com", "BBAPPROVE_AUTHOR_EMAIL=bot@example.com"), RepoDir: fx.repo}
	}},
	{"config-missing", []string{"error:config-missing"}, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"config-file-lacks-setting", nil, func(t *testing.T, fx *fixture) Options {
		p := filepath.Join(fx.repo, "c.toml")
		os.WriteFile(p, []byte("repository = \"https://bb.example.com\"\n"), 0o600)
		return Options{Args: []string{"--config", p}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"config-malformed", nil, func(t *testing.T, fx *fixture) Options {
		p := filepath.Join(fx.repo, "c.toml")
		os.WriteFile(p, []byte("repository = [oops\n"), 0o600)
		return Options{Args: []string{"--config", p}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"token-flag-removed", []string{"error:token-flag"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--token", "abc"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	// argument errors
	{"unknown-option", []string{"error:unknown-option"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--bogus"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"unknown-help-topic", []string{"error:unknown-topic", "help"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"help", "nosuchtopic"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"flag-needs-value-disallow", []string{"--disallow"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--disallow"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"flag-needs-value-token-name", []string{"--token-name"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--token-name"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"flag-needs-value-config", []string{"--config"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--config"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"doctor-with-apply", []string{"doctor"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"doctor", "--apply"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"offline-without-doctor", []string{"--offline"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--offline"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"not-a-pr-url", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"nonsense"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"pre-without-update", []string{"--pre"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--pre"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"force-without-init", []string{"--force"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"--force"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"tui-not-implemented", []string{"-i"}, func(t *testing.T, fx *fixture) Options {
		return Options{Args: []string{"-i"}, Environ: fx.env(), RepoDir: fx.repo}
	}},
	{"ignore-list-unreadable", nil, func(t *testing.T, fx *fixture) Options {
		ign := filepath.Join(fx.base, "ignore")
		os.Mkdir(ign, 0o755) // a directory cannot be read as a file
		return Options{Environ: fx.env("BBAPPROVE_TOKEN=t", "BBAPPROVE_REPOSITORY=https://bb.example.com", "BBAPPROVE_AUTHOR_EMAIL=bot@example.com"),
			RepoDir: fx.repo, IgnorePath: ign}
	}},
	{"adapter-failure", nil, func(t *testing.T, fx *fixture) Options {
		return Options{Environ: fx.env("BBAPPROVE_TOKEN=t", "BBAPPROVE_ADAPTER="+testfix.Adapter(), "FAKE_BODY=echo 'boom: list failed' >&2; exit 1", "BBAPPROVE_REPOSITORY=https://bb.example.com", "BBAPPROVE_AUTHOR_EMAIL=bot@example.com"),
			RepoDir: fx.repo, IgnorePath: filepath.Join(fx.base, "none")}
	}},
}

func TestGoldenPlainPaths(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range pathCases {
		if seen[c.name] {
			t.Fatalf("duplicate case %s", c.name)
		}
		seen[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			checkGolden(t, "path_"+c.name+".golden", runPathCase(t, c, "NO_COLOR=1").String())
		})
	}
}
