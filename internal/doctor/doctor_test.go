package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bbapprove/internal/testfix"
	"bbapprove/internal/update"
)

const validCfg = "repository = \"https://bb.corp.test\"\nauthor_email = \"bot@corp.test\"\ndisallow = [\"Left-Pad\"]\n"

// fakeRunner never executes anything; it records calls and answers gh.
type fakeRunner struct {
	calls     []string
	releases  string // gh release list output
	releaseEr error
	authErr   error
}

func (f *fakeRunner) Run(_ context.Context, c update.Cmd) ([]byte, error) {
	f.calls = append(f.calls, c.Name+" "+strings.Join(c.Args, " "))
	switch {
	case c.Name == "gh" && len(c.Args) > 0 && c.Args[0] == "auth":
		return nil, f.authErr
	case c.Name == "gh" && len(c.Args) > 0 && c.Args[0] == "release":
		return []byte(f.releases), f.releaseEr
	}
	return nil, errors.New("unexpected command " + c.Name)
}

func (f *fakeRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// rig is one doctor scenario in a scratch tree.
type rig struct {
	t     *testing.T
	root  string // install root
	xdg   string
	env   []string
	opts  Options
	run   *fakeRunner
	tools map[string]string // name -> path
}

func newRig(t *testing.T) *rig {
	t.Helper()
	testfix.RequireUnixModes(t) // the rig asserts on file modes and the adapter's exec bit
	return newRigAnyOS(t)
}

// newRigAnyOS is newRig for tests that run on every OS (they report GOOS "windows" and ignore modes).
func newRigAnyOS(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, root: t.TempDir(), xdg: t.TempDir(), run: &fakeRunner{
		releases: `[{"tagName":"v1.0.0","isPrerelease":false,"isDraft":false}]`,
	}, tools: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "op": "/usr/bin/op"}}
	ad := filepath.Join(r.root, "adapters", "bitbucket")
	os.MkdirAll(filepath.Dir(ad), 0o755)
	os.WriteFile(ad, []byte("#!/bin/sh\n"), 0o755)
	r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg, "BBAPPROVE_TOKEN=tok-" + canary}
	return r
}

func (r *rig) writeXDG(content string) string {
	p := filepath.Join(r.xdg, "bbapprove", "config.toml")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(content), 0o600)
	return p
}

func (r *rig) writeRepo(content string) string {
	p := filepath.Join(r.root, "bbapprove.config.toml")
	os.WriteFile(p, []byte(content), 0o600)
	return p
}

func (r *rig) ignorePath() string { return filepath.Join(r.xdg, "bbapprove", "ignore") }

func (r *rig) doctor(mod func(*Options)) (int, string) {
	r.t.Helper()
	var out bytes.Buffer
	bin := filepath.Join(r.root, "bin", "bbapprove-go")
	o := Options{
		Environ: r.env, Stdout: &out, Stderr: &out, Runner: r.run,
		LookPath: func(n string) (string, error) {
			if p, ok := r.tools[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Executable: func() (string, error) { return bin, nil },
		EvalLinks:  func(p string) (string, error) { return p, nil },
		Version:    "v1.0.0", GOOS: "linux", GOARCH: "amd64", GoVersion: "go1.test",
		InstallRoot: r.root, IgnorePath: r.ignorePath(),
	}
	if mod != nil {
		mod(&o)
	}
	code := Run(context.Background(), o)
	return code, out.String()
}

// doctorV runs the full report (bbapprove doctor -v).
func (r *rig) doctorV(mod func(*Options)) (int, string) {
	r.t.Helper()
	return r.doctor(func(o *Options) {
		o.Verbose = true
		if mod != nil {
			mod(o)
		}
	})
}

func mustContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(out, s) {
			t.Errorf("output must not contain %q:\n%s", s, out)
		}
	}
}

func TestNoConfigFile(t *testing.T) {
	r := newRig(t)
	code, out := r.doctorV(nil)
	if code != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
	mustContain(t, out, "FAIL config   no config file found — run: bbapprove --init", "cp bbapprove.config.template bbapprove.config.toml",
		"1 problem(s) found\n", "bbapprove v1.0.0 (linux/amd64, go1.test) · release archive", "root     "+r.root)
	if strings.Count(out, "not found  ") != 2 {
		t.Errorf("both path candidates must read not found:\n%s", out)
	}
}

func TestEnvSettingsWithoutFileIsWarningOnly(t *testing.T) {
	r := newRig(t)
	r.env = append(r.env, "BBAPPROVE_REPOSITORY=https://bb.corp.test", "BBAPPROVE_AUTHOR_EMAIL=bot@corp.test")
	code, out := r.doctorV(nil)
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	mustContain(t, out, "no config file: all settings come from the environment", "all good")
}

func TestConfigMechanisms(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *rig) (flag string, want string, shadowed []string)
		via   string
	}{
		{"xdg", func(r *rig) (string, string, []string) { return "", r.writeXDG(validCfg), nil }, "XDG config"},
		{"repo root beats xdg", func(r *rig) (string, string, []string) {
			x := r.writeXDG(validCfg)
			return "", r.writeRepo(validCfg), []string{x}
		}, "repo root"},
		{"BBAPPROVE_CONFIG beats repo and xdg", func(r *rig) (string, string, []string) {
			x, rp := r.writeXDG(validCfg), r.writeRepo(validCfg)
			p := filepath.Join(t.TempDir(), "env.toml")
			os.WriteFile(p, []byte(validCfg), 0o600)
			r.env = append(r.env, "BBAPPROVE_CONFIG="+p)
			return "", p, []string{rp, x}
		}, "BBAPPROVE_CONFIG"},
		{"flag beats everything", func(r *rig) (string, string, []string) {
			x, rp := r.writeXDG(validCfg), r.writeRepo(validCfg)
			e := filepath.Join(t.TempDir(), "env.toml")
			os.WriteFile(e, []byte(validCfg), 0o600)
			r.env = append(r.env, "BBAPPROVE_CONFIG="+e)
			p := filepath.Join(t.TempDir(), "flag.toml")
			os.WriteFile(p, []byte(validCfg), 0o600)
			return p, p, []string{e, rp, x}
		}, "--config flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			flag, want, shadowed := tc.setup(r)
			code, out := r.doctorV(func(o *Options) { o.ConfigFlag = flag; o.Environ = r.env })
			if code != 0 {
				t.Fatalf("code %d\n%s", code, out)
			}
			mustContain(t, out, "ok   config   "+want+" ("+tc.via+"), valid TOML, mode 0600\n", "[file]", "all good")
			if n := strings.Count(out, want); n != 1 {
				t.Errorf("the config path appears %d times, want once:\n%s", n, out)
			}
			mustContain(t, out, " used\n")
			for _, s := range shadowed {
				mustContain(t, out, "shadowed  "+s)
			}
		})
	}
}

func TestExplicitConfigMissing(t *testing.T) {
	r := newRig(t)
	r.env = append(r.env, "BBAPPROVE_CONFIG=/nonexistent/x.toml")
	code, out := r.doctorV(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "FAIL config   ", "/nonexistent/x.toml", "not found  /nonexistent/x.toml", "fix the file")
}

func TestMalformedTOML(t *testing.T) {
	r := newRig(t)
	p := r.writeXDG("repository = \"x\"\nauthor_email = = broken\n")
	code, out := r.doctorV(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "FAIL config   invalid TOML in "+p+" at line 2", "1 problem(s) found\n")
}

func TestPlaceholderValues(t *testing.T) {
	r := newRig(t)
	tmpl, _ := os.ReadFile("../config/bbapprove.config.template")
	p := r.writeXDG(string(tmpl))
	code, out := r.doctorV(nil)
	if code != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
	mustContain(t, out, "FAIL config   "+p+" (XDG config), valid TOML, mode 0600, the file is unchanged from the template — edit the file and set your own values")
}

func TestMissingRepositoryAndAuthor(t *testing.T) {
	r := newRig(t)
	r.writeXDG("type = \"bitbucket\"\n")
	code, out := r.doctorV(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "FAIL settings repository is not set — set repository in "+r.xdg, "FAIL settings author_email is not set", "2 problem(s) found\n")
}

func TestFileModeNote(t *testing.T) {
	r := newRig(t)
	p := r.writeXDG(validCfg)
	os.Chmod(p, 0o644)
	code, out := r.doctorV(nil)
	if code != 0 {
		t.Fatalf("a loose mode is only a note, code %d", code)
	}
	mustContain(t, out, "mode 0644 (default 0600")
}

func TestIgnoreList(t *testing.T) {
	t.Run("entries listed", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.WriteFile(r.ignorePath(), []byte("# mine\n@some/library\nlodash\n"), 0o600)
		code, out := r.doctorV(nil)
		if code != 0 {
			t.Fatalf("code %d\n%s", code, out)
		}
		mustContain(t, out, "ok   ignore   "+r.ignorePath()+", 2 entries, 0600\n",
			"     rows matching these entries are shown as IGNORED and never acted on; edit the file or press i on an ignored row in the TUI to remove an entry\n")
		mustNotContain(t, out, "@some/library", "lodash")
	})
	t.Run("absent", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		code, out := r.doctorV(nil)
		if code != 0 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, r.ignorePath()+", does not exist (nothing is ignored)", "rows matching these entries")
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read anything")
		}
		r := newRig(t)
		r.writeXDG(validCfg)
		os.WriteFile(r.ignorePath(), []byte("lodash\n"), 0o600)
		os.Chmod(r.ignorePath(), 0)
		t.Cleanup(func() { os.Chmod(r.ignorePath(), 0o600) })
		code, out := r.doctorV(nil)
		if code != 1 {
			t.Fatalf("code %d\n%s", code, out)
		}
		mustContain(t, out, "FAIL ignore   "+r.ignorePath()+" exists but is unreadable", "fails closed", "chmod 600")
	})
	t.Run("directory in the way", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.MkdirAll(r.ignorePath(), 0o755)
		code, out := r.doctorV(nil)
		if code != 1 {
			t.Fatalf("code %d\n%s", code, out)
		}
		mustContain(t, out, "is unreadable")
	})
	t.Run("a long list stays one line", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		var b strings.Builder
		for i := 0; i < 53; i++ {
			fmt.Fprintf(&b, "dep-%02d\n", i)
		}
		os.WriteFile(r.ignorePath(), []byte(b.String()), 0o600)
		_, out := r.doctorV(nil)
		mustContain(t, out, ", 53 entries, 0600\n")
		mustNotContain(t, out, "dep-00")
	})
}

func TestDisallowSources(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	df := filepath.Join(t.TempDir(), "dis.txt")
	os.WriteFile(df, []byte("# c\nFile-Dep\n"), 0o644)
	r.env = append(r.env, "BBAPPROVE_DISALLOW=env-dep, other-dep", "BBAPPROVE_DISALLOW_FILE="+df)
	_, out := r.doctorV(nil)
	mustContain(t, out, "disallow:              file-dep, env-dep, other-dep, left-pad  [env, disallow_file, file]\n", "disallow_file:         "+df+"  [env]\n")
	mustNotContain(t, out, "disallow (matching", "--disallow", "flag only")
}

const canary = "CANARY-SECRET-123"

func TestTokenSection(t *testing.T) {
	const secret = "s3cr3t-token-VALUE-123"
	for _, tc := range []struct {
		name    string
		cfg     string
		env     []string
		tools   map[string]string
		exit    int
		wants   []string
		unwants []string
	}{
		{"env token wins", validCfg, []string{"BBAPPROVE_TOKEN=" + secret, "BBAPPROVE_TOKEN_NAME=MY_ITEM"}, nil, 0,
			[]string{"BBAPPROVE_TOKEN set", `item "MY_ITEM" [env] is ignored`}, []string{"not looked up"}},
		{"name from env", validCfg, []string{"BBAPPROVE_TOKEN_NAME=MY_ITEM"}, nil, 0,
			[]string{`1Password item "MY_ITEM" [env] (not looked up by doctor)`}, []string{"NOT found", "BBAPPROVE_TOKEN set"}},
		{"name from file", validCfg + "user_token_name = \"FILE_ITEM\"\n", nil, nil, 0,
			[]string{`1Password item "FILE_ITEM" [file] (not looked up by doctor)`}, nil},
		{"name but no op", validCfg, []string{"BBAPPROVE_TOKEN_NAME=MY_ITEM"}, map[string]string{"git": "/usr/bin/git"}, 0,
			[]string{`item "MY_ITEM"`, "op (1Password CLI) NOT found on PATH"}, nil},
		{"neither", validCfg, nil, nil, 1,
			[]string{"FAIL token    no token: BBAPPROVE_TOKEN is not set and no 1Password item name is configured — export BBAPPROVE_TOKEN=<token>", "1 problem(s) found\n"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.writeXDG(tc.cfg)
			r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg} // no token source from the rig
			r.env = append(r.env, tc.env...)
			if tc.tools != nil {
				r.tools = tc.tools
			}
			code, out := r.doctorV(nil)
			if code != tc.exit {
				t.Errorf("exit %d, want %d", code, tc.exit)
			}
			mustContain(t, out, tc.wants...)
			mustNotContain(t, out, append(tc.unwants, secret, "s3cr3t", canary)...)
			for _, c := range r.run.calls {
				if strings.HasPrefix(c, "op") || strings.Contains(c, secret) {
					t.Errorf("runner called with %q", c)
				}
			}
		})
	}
}

func TestUpdateOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		mod     func(f *fakeRunner)
		want    string
	}{
		{"up to date", "v1.0.0", nil, "up to date (v1.0.0)"},
		{"available", "v0.9.0", nil, "update available: v1.0.0 (run: bbapprove --update)"},
		{"cannot check", "v1.0.0", func(f *fakeRunner) { f.releaseEr = errors.New("network down") }, "could not check: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.writeXDG(validCfg)
			if tc.mod != nil {
				tc.mod(r.run)
			}
			code, out := r.doctorV(func(o *Options) { o.Version = tc.version })
			if code != 0 {
				t.Errorf("an update problem is never a FAIL, code %d", code)
			}
			mustContain(t, out, tc.want)
		})
	}
}

func TestOfflineSkipsNetwork(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	_, out := r.doctorV(func(o *Options) { o.Offline = true })
	mustContain(t, out, "skipped (--offline)")
	if r.run.called("gh release") || r.run.called("git") {
		t.Errorf("offline made update calls: %v", r.run.calls)
	}
	if !r.run.called("gh auth status") {
		t.Errorf("gh auth status is local and still expected: %v", r.run.calls)
	}
}

func TestGhAuth(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.run.authErr = errors.New("not logged in")
	_, out := r.doctorV(func(o *Options) { o.Offline = true })
	mustContain(t, out, "gh /usr/bin/gh (not logged in)")
	// gh absent: never run
	r2 := newRig(t)
	r2.writeXDG(validCfg)
	delete(r2.tools, "gh")
	_, out = r2.doctorV(func(o *Options) { o.Offline = true })
	mustContain(t, out, "gh not found (needed for --outdated / --update)")
	if r2.run.called("gh") {
		t.Errorf("gh run although missing: %v", r2.run.calls)
	}
}

func TestPathShadowing(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.tools["bbapprove"] = "/opt/other/bbapprove"
	code, out := r.doctorV(nil)
	if code != 0 {
		t.Errorf("shadowing is only a warning, code %d", code)
	}
	mustContain(t, out, "warn PATH     shadowed: /opt/other/bbapprove is not this binary\n", "all good")
	if n := strings.Count(out, "/opt/other/bbapprove"); n != 1 {
		t.Errorf("the shadowing path appears %d times, want once:\n%s", n, out)
	}
	// the launcher next to the binary is fine
	r.tools["bbapprove"] = filepath.Join(r.root, "bin", "bbapprove")
	_, out = r.doctorV(nil)
	mustNotContain(t, out, "shadowing")
}

func TestAdapterChecks(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	os.Chmod(filepath.Join(r.root, "adapters", "bitbucket"), 0o644)
	code, out := r.doctorV(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "is not executable", "FAIL bitbucket adapter ", "chmod +x")
	os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
	_, out = r.doctorV(nil)
	mustContain(t, out, "does not exist", "FAIL bitbucket adapter ")
	os.WriteFile(filepath.Join(r.root, "adapters", "bitbucket"), []byte("#!/bin/sh\n"), 0o755)
	code, out = r.doctorV(nil)
	mustNotContain(t, out, "\nadapter:\n", "ok   adapter", "adapter exists")
	if code != 0 {
		t.Errorf("code %d", code)
	}
}

func TestEnvironmentSection(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.env = append(r.env, "BBAPPROVE_JOBS=3", "BBAPPROVE_TOKEN=zzz-secret", "EDITOR=vi")
	_, out := r.doctorV(nil)
	mustContain(t, out, "     env      BBAPPROVE_JOBS=3, NO_COLOR=1, XDG_CONFIG_HOME="+r.xdg+", EDITOR=vi\n")
	mustNotContain(t, out, "zzz-secret", "BBAPPROVE_TOKEN=", "VISUAL", "FORCE_COLOR")
}

func TestPlainOutputHasNoEscapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		tty  bool
		env  []string
	}{
		{"NO_COLOR on a tty", true, []string{"NO_COLOR=1"}},
		{"not a tty", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.env = append([]string{"XDG_CONFIG_HOME=" + r.xdg}, tc.env...)
			r.writeXDG(validCfg)
			_, out := r.doctorV(func(o *Options) { o.IsTTY = tc.tty; o.Environ = r.env })
			mustNotContain(t, out, "\033", "✓", "✗")
			mustContain(t, out, "ok   ")
		})
	}
	// colour only when the app would use it
	r := newRig(t)
	r.env = []string{"XDG_CONFIG_HOME=" + r.xdg}
	r.writeXDG(validCfg)
	_, out := r.doctorV(func(o *Options) { o.IsTTY = true; o.Environ = r.env })
	mustContain(t, out, "\033[32m✓")
}

func TestExitCodeAllGood(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	code, out := r.doctorV(nil)
	if code != 0 || !strings.HasSuffix(out, "all good\n") {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestSettingsListDeleteSourceBranch(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg + "delete_source_branch = false\n")
	_, out := r.doctorV(nil)
	mustContain(t, out, "delete_source_branch:  false  [file]")
	r = newRig(t)
	r.writeXDG(validCfg)
	r.env = append(r.env, "BBAPPROVE_DELETE_SOURCE_BRANCH=1")
	_, out = r.doctorV(nil)
	mustContain(t, out, "delete_source_branch:  true  [env]")
}
