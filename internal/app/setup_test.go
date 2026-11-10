package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bbapprove/internal/config"
	"bbapprove/internal/testfix"
)

// validCfg differs from the template's example values, which first-run setup treats as unedited.
const validCfg = "repository = \"https://bb.corp.test\"\nauthor_email = \"bot@corp.test\"\n"

// setupRig is a first-run scenario: empty HOME/XDG, no repo-root config, fake adapter.
type setupRig struct {
	t       *testing.T
	xdg     string
	repo    string
	env     []string
	editLog string
}

func newRig(t *testing.T, useXDG bool, editorBody string) *setupRig {
	t.Helper()
	baseEnv(t) // fake adapter env is rebuilt below; this also isolates XDG_CONFIG_HOME for the process
	env, _ := baseEnv(t)
	var kept []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "BBAPPROVE_REPOSITORY=") && !strings.HasPrefix(kv, "BBAPPROVE_AUTHOR_EMAIL=") {
			kept = append(kept, kv)
		}
	}
	r := &setupRig{t: t, repo: t.TempDir()}
	home := t.TempDir()
	testfix.SetHome(t, home)
	if useXDG {
		r.xdg = filepath.Join(t.TempDir(), "xdgdir")
		kept = append(kept, "XDG_CONFIG_HOME="+r.xdg)
		t.Setenv("XDG_CONFIG_HOME", r.xdg)
	} else {
		r.xdg = filepath.Join(home, ".config")
		kept = append(kept, "XDG_CONFIG_HOME=", "HOME="+home)
		t.Setenv("XDG_CONFIG_HOME", "")
	}
	r.editLog = filepath.Join(t.TempDir(), "editor.log")
	if editorBody != "" {
		// The editor runs with the process environment (ExecEditor sets no Env), hence t.Setenv.
		t.Setenv("FAKE_EDITOR_LOG", r.editLog)
		t.Setenv("FAKE_EDITOR_BODY", strings.ReplaceAll(editorBody, "@LOG@", `"$FAKE_EDITOR_LOG"`))
		kept = append(kept, "EDITOR="+testfix.Editor()+" -w")
	}
	r.env = kept
	return r
}

func (r *setupRig) path() string { return filepath.Join(r.xdg, "bbapprove", "config.toml") }

func (r *setupRig) run(tty bool, stdin string, args ...string) (int, string, string) {
	var so, se bytes.Buffer
	code := Run(context.Background(), Options{
		Args: args, Environ: r.env, RepoDir: r.repo, Stdout: &so, Stderr: &se,
		Interactive: func() bool { return tty }, Stdin: strings.NewReader(stdin),
	})
	return code, so.String(), se.String()
}

const writeValid = `printf '%s' '` + validCfg + `' > "$2"`

func TestFirstRunNonTTY(t *testing.T) {
	for _, useXDG := range []bool{true, false} {
		r := newRig(t, useXDG, "")
		code, out, errOut := r.run(false, "y\n")
		if code != 1 || out != "" {
			t.Errorf("xdg=%v: code %d out %q", useXDG, code, out)
		}
		for _, want := range []string{r.path(), "mkdir -p " + filepath.Dir(r.path()), "cp bbapprove.config.template " + r.path(), "bbapprove --init", `"repository" is not set`} {
			if !strings.Contains(errOut, want) {
				t.Errorf("xdg=%v: stderr lacks %q:\n%s", useXDG, want, errOut)
			}
		}
		if _, err := os.Stat(r.path()); err == nil {
			t.Errorf("xdg=%v: file was written without a TTY", useXDG)
		}
	}
}

func TestFirstRunTTY(t *testing.T) {
	for _, tc := range []struct {
		name, stdin, editor string
		wantCode            int
		wantFile            bool
		wantErr, wantOut    []string
	}{
		{"yes continues", "y\n", writeValid, 0, true, []string{"no config file found.", "Create ", "[Y/n]", "config OK"}, []string{"Dry run"}},
		{"enter is yes", "\n", writeValid, 0, true, []string{"config OK"}, []string{"Dry run"}},
		{"no", "n\n", writeValid, 1, false, []string{"Not creating it", "bbapprove --init"}, nil},
		{"eof is no", "", writeValid, 1, false, []string{"Not creating it"}, nil},
		{"editor leaves it invalid", "y\n", `printf 'type = "bitbucket"\n' > "$2"`, 1, true, []string{"\"repository\" is not set", "config file ", "does not set it", "run the same command again"}, nil},
		{"no editor configured", "y\n", "", 1, true, []string{"No $VISUAL or $EDITOR is set", "Edit "}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, true, tc.editor)
			code, out, errOut := r.run(true, tc.stdin)
			if code != tc.wantCode {
				t.Fatalf("code %d; out %q err %q", code, out, errOut)
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr lacks %q:\n%s", w, errOut)
				}
			}
			if strings.Contains(errOut, r.path()) != true && tc.name != "eof is no" && tc.name != "no" {
				t.Errorf("stderr lacks path %s:\n%s", r.path(), errOut)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			_, err := os.Stat(r.path())
			if (err == nil) != tc.wantFile {
				t.Errorf("file exists=%v, want %v", err == nil, tc.wantFile)
			}
			if tc.wantFile {
				st, _ := os.Stat(r.path())
				if st.Mode().Perm() != 0o600 {
					t.Errorf("mode %v", st.Mode().Perm())
				}
			}
			if tc.editor == writeValid {
				if tc.wantFile {
					if b, _ := os.ReadFile(r.editLog); !strings.Contains(string(b), "-w "+r.path()) {
						t.Errorf("editor not invoked with -w and the path: %q", b)
					}
				} else if _, err := os.Stat(r.editLog); err == nil {
					t.Error("editor ran although the user declined")
				}
			}
		})
	}
}

func TestFirstRunContinuesWithFakeAdapter(t *testing.T) {
	r := newRig(t, true, writeValid)
	code, out, errOut := r.run(true, "\n")
	if code != 0 || !strings.Contains(out, "Dry run") || !strings.Contains(errOut, "config OK") {
		t.Fatalf("code %d\nout:\n%s\nerr:\n%s", code, out, errOut)
	}
}

func TestFirstRunInjectedEditor(t *testing.T) {
	r := newRig(t, true, "")
	r.env = append(r.env, "VISUAL=code -w")
	var gotArgv []string
	var gotPath string
	var so, se bytes.Buffer
	code := Run(context.Background(), Options{
		Args: nil, Environ: r.env, RepoDir: r.repo, Stdout: &so, Stderr: &se,
		Interactive: func() bool { return true }, Stdin: strings.NewReader("y\n"),
		Editor: func(_ context.Context, argv []string, path string) error {
			gotArgv, gotPath = argv, path
			return os.WriteFile(path, []byte(validCfg), 0o600)
		},
	})
	if code != 0 || strings.Join(gotArgv, " ") != "code -w" || gotPath != r.path() {
		t.Fatalf("code %d argv %q path %q\n%s", code, gotArgv, gotPath, se.String())
	}
}

func TestExistingMalformedConfigUnchanged(t *testing.T) {
	r := newRig(t, true, writeValid)
	os.MkdirAll(filepath.Dir(r.path()), 0o700)
	os.WriteFile(r.path(), []byte("repository = [oops"), 0o600)
	code, _, errOut := r.run(true, "y\n")
	if code != 1 || !strings.Contains(errOut, "invalid TOML in "+r.path()) || strings.Contains(errOut, "[Y/n]") {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(r.path()); string(b) != "repository = [oops" {
		t.Error("existing file modified")
	}
	// incomplete (not malformed) existing config: old error, no prompt, no edit
	os.WriteFile(r.path(), []byte("type = \"bitbucket\"\n"), 0o600)
	code, _, errOut = r.run(true, "y\n")
	if code != 1 || !strings.Contains(errOut, "does not set it") || strings.Contains(errOut, "[Y/n]") {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if _, err := os.Stat(r.editLog); err == nil {
		t.Error("editor ran for an existing config")
	}
}

func TestInit(t *testing.T) {
	r := newRig(t, true, "true")
	// non-TTY: writes, no editor, next step shown
	code, out, _ := r.run(false, "", "--init")
	if code != 0 || !strings.Contains(out, r.path()) || !strings.Contains(out, "bbapprove --print-config") {
		t.Fatalf("code %d out %q", code, out)
	}
	if b, _ := os.ReadFile(r.path()); string(b) != config.Template {
		t.Error("template content differs")
	}
	if _, err := os.Stat(r.editLog); err == nil {
		t.Error("editor ran without a TTY")
	}
	// refuses to overwrite
	os.WriteFile(r.path(), []byte("mine"), 0o600)
	code, _, errOut := r.run(false, "", "--init")
	if code != 1 || !strings.Contains(errOut, "already exists") || !strings.Contains(errOut, "--force") {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if b, _ := os.ReadFile(r.path()); string(b) != "mine" {
		t.Error("--init overwrote")
	}
	// --force overwrites, opens the editor on a TTY
	code, _, _ = r.run(true, "", "--init", "--force")
	if b, _ := os.ReadFile(r.path()); code != 0 || string(b) != config.Template {
		t.Fatalf("force: code %d", code)
	}
	if b, _ := os.ReadFile(r.editLog); !strings.Contains(string(b), r.path()) {
		t.Errorf("editor log %q", b)
	}
}

func TestInitExplicitConfigPathAndForceAlone(t *testing.T) {
	r := newRig(t, true, "")
	p := filepath.Join(t.TempDir(), "sub", "my.toml")
	code, out, _ := r.run(false, "", "--init", "--config", p)
	if code != 0 || !strings.Contains(out, p) {
		t.Fatalf("code %d out %q", code, out)
	}
	if b, _ := os.ReadFile(p); string(b) != config.Template {
		t.Error("explicit path not written")
	}
	if _, err := os.Stat(r.path()); err == nil {
		t.Error("default path written too")
	}
	if code, _, _ := r.run(false, "", "--force"); code != 2 {
		t.Errorf("--force alone: code %d", code)
	}
}

func TestPrintConfigHintWithoutConfig(t *testing.T) {
	r := newRig(t, true, "")
	code, out, _ := r.run(true, "", "--print-config")
	if code != 0 || !strings.Contains(out, "bbapprove --init") {
		t.Fatalf("code %d out %q", code, out)
	}
	r.run(false, "", "--init")
	_, out, _ = r.run(false, "", "--print-config")
	if strings.Contains(out, "bbapprove --init") {
		t.Errorf("hint shown although a config exists: %q", out)
	}
}

func TestHelpMentionsInit(t *testing.T) {
	if !strings.Contains(usage("x"), "--init [--force]") {
		t.Error("usage lacks --init")
	}
}

// writeLine returns an editor body that rewrites the config with the given TOML (printf-safe).
func writeLine(toml string) string { return `printf '%s' '` + toml + `' > "$2"` }

func TestFirstRunPlaceholders(t *testing.T) {
	repoOnly := "repository = \"https://bitbucket.example.com\"\nauthor_email = \"me@corp.test\"\n"
	emailOnly := "repository = \"https://bb.corp.test\"\nauthor_email = \"renovate-bot@example.com\"\n"
	for _, tc := range []struct {
		name, stdin, editor string
		wantCode, wantRuns  int
		wantErr             []string
	}{
		{"edited proceeds, no re-prompt", "y\n", writeValid, 0, 1, []string{"config OK"}},
		{"unchanged template re-prompts then proceeds", "y\ny\n",
			`if [ "$(wc -l < @LOG@)" -ge 2 ]; then ` + writeValid + `; fi`, 0, 2,
			[]string{"the file is unchanged from the template", "Edit again? [Y/n]", "config OK"}},
		{"repository still example, user declines", "y\nn\n", writeLine(repoOnly), 1, 1,
			[]string{"repository is still the example value https://bitbucket.example.com", "left in place", "bbapprove --init --force"}},
		{"author_email still example", "y\nn\n", writeLine(emailOnly), 1, 1,
			[]string{"author_email is still the example value renovate-bot@example.com", "Edit again? [Y/n]"}},
		{"three rounds without change", "y\n\n\n\n", "true", 1, 3,
			[]string{"Giving up after 3 rounds", "bbapprove --init --force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, true, tc.editor)
			code, _, errOut := r.run(true, tc.stdin)
			if code != tc.wantCode {
				t.Fatalf("code %d, want %d:\n%s", code, tc.wantCode, errOut)
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr lacks %q:\n%s", w, errOut)
				}
			}
			b, _ := os.ReadFile(r.editLog)
			if n := strings.Count(string(b), "\n"); n != tc.wantRuns {
				t.Errorf("editor ran %d times, want %d", n, tc.wantRuns)
			}
			if _, err := os.Stat(r.path()); err != nil {
				t.Errorf("file must stay in place: %v", err)
			}
		})
	}
}

func TestInitTTYWarnsAboutPlaceholders(t *testing.T) {
	r := newRig(t, true, "true") // editor changes nothing
	code, out, _ := r.run(true, "", "--init")
	for _, w := range []string{"warning: the file is unchanged from the template", "bbapprove --print-config"} {
		if code != 0 || !strings.Contains(out, w) {
			t.Fatalf("code %d, stdout lacks %q:\n%s", code, w, out)
		}
	}
	r = newRig(t, true, writeValid)
	code, out, _ = r.run(true, "", "--init")
	if code != 0 || strings.Contains(out, "warning") {
		t.Fatalf("edited init warned: code %d\n%s", code, out)
	}
	// non-TTY init does not run the editor and does not warn
	r = newRig(t, true, "true")
	if code, out, _ = r.run(false, "", "--init"); code != 0 || strings.Contains(out, "warning") {
		t.Fatalf("non-TTY: code %d\n%s", code, out)
	}
}

func TestInitHonoursBBAPPROVE_CONFIG(t *testing.T) {
	r := newRig(t, true, "")
	envPath := filepath.Join(t.TempDir(), "env", "c.toml")
	flagPath := filepath.Join(t.TempDir(), "flag.toml")
	r.env = append(r.env, "BBAPPROVE_CONFIG="+envPath)
	if code, out, _ := r.run(false, "", "--init"); code != 0 || !strings.Contains(out, envPath) {
		t.Fatalf("env: code %d out %q", code, out)
	}
	if b, _ := os.ReadFile(envPath); string(b) != config.Template {
		t.Error("env path not written")
	}
	if code, _, _ := r.run(false, "", "--init", "--config", flagPath); code != 0 {
		t.Fatal("flag run failed")
	}
	if _, err := os.Stat(flagPath); err != nil {
		t.Error("flag must win over env")
	}
	if _, err := os.Stat(r.path()); err == nil {
		t.Error("default path written although env/flag given")
	}
}
