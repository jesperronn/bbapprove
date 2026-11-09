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
	code, out := r.doctor(nil)
	if code != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
	mustContain(t, out, "FAIL no config file found", "bbapprove --init", "cp bbapprove.config.template bbapprove.config.toml",
		"1 problem(s) found", "bbapprove v1.0.0  (linux/amd64, go1.test)", "install: ", "release archive")
	if strings.Count(out, "[not found]") != 2 {
		t.Errorf("both path candidates must read not found:\n%s", out)
	}
}

func TestEnvSettingsWithoutFileIsWarningOnly(t *testing.T) {
	r := newRig(t)
	r.env = append(r.env, "BBAPPROVE_REPOSITORY=https://bb.corp.test", "BBAPPROVE_AUTHOR_EMAIL=bot@corp.test")
	code, out := r.doctor(nil)
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
			code, out := r.doctor(func(o *Options) { o.ConfigFlag = flag; o.Environ = r.env })
			if code != 0 {
				t.Fatalf("code %d\n%s", code, out)
			}
			mustContain(t, out, want+"  [found, used]", "in use:       "+want+" (via "+tc.via+")", "valid TOML", "[file]", "all good")
			for _, s := range shadowed {
				mustContain(t, out, s+"  [found, shadowed by "+want+"]")
			}
		})
	}
}

func TestExplicitConfigMissing(t *testing.T) {
	r := newRig(t)
	r.env = append(r.env, "BBAPPROVE_CONFIG=/nonexistent/x.toml")
	code, out := r.doctor(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "/nonexistent/x.toml  [NOT FOUND, but selected explicitly]", "FAIL config: ", "/nonexistent/x.toml")
}

func TestMalformedTOML(t *testing.T) {
	r := newRig(t)
	p := r.writeXDG("repository = \"x\"\nauthor_email = = broken\n")
	code, out := r.doctor(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "FAIL config: invalid TOML in "+p+" at line 2", "1 problem(s) found")
}

func TestPlaceholderValues(t *testing.T) {
	r := newRig(t)
	tmpl, _ := os.ReadFile("../config/bbapprove.config.template")
	p := r.writeXDG(string(tmpl))
	code, out := r.doctor(nil)
	if code != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
	mustContain(t, out, "FAIL config "+p+": the file is unchanged from the template", "edit the file")
}

func TestMissingRepositoryAndAuthor(t *testing.T) {
	r := newRig(t)
	r.writeXDG("type = \"bitbucket\"\n")
	code, out := r.doctor(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "FAIL repository is not set", "FAIL author_email is not set", "2 problem(s) found")
}

func TestFileModeNote(t *testing.T) {
	r := newRig(t)
	p := r.writeXDG(validCfg)
	os.Chmod(p, 0o644)
	code, out := r.doctor(nil)
	if code != 0 {
		t.Fatalf("a loose mode is only a note, code %d", code)
	}
	mustContain(t, out, "warn", "0644: readable by group/other (default is 0600")
}

func TestIgnoreList(t *testing.T) {
	t.Run("entries listed", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.WriteFile(r.ignorePath(), []byte("# mine\n@some/library\nlodash\n"), 0o600)
		code, out := r.doctor(nil)
		if code != 0 {
			t.Fatalf("code %d\n%s", code, out)
		}
		mustContain(t, out, r.ignorePath(), "entries:      2", "@some/library", "lodash",
			"rows matching these entries are shown as IGNORED and never acted on; edit the file or press i on an ignored row in the TUI to remove an entry")
	})
	t.Run("absent", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		code, out := r.doctor(nil)
		if code != 0 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, r.ignorePath(), "does not exist: no dependency is ignored")
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
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d\n%s", code, out)
		}
		mustContain(t, out, "FAIL ignore list "+r.ignorePath()+" is unreadable", "fails closed", "chmod 600")
	})
	t.Run("directory in the way", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.MkdirAll(r.ignorePath(), 0o755)
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d\n%s", code, out)
		}
		mustContain(t, out, "is unreadable")
	})
	t.Run("truncated at 50", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		var b strings.Builder
		for i := 0; i < 53; i++ {
			fmt.Fprintf(&b, "dep-%02d\n", i)
		}
		os.WriteFile(r.ignorePath(), []byte(b.String()), 0o600)
		_, out := r.doctor(nil)
		mustContain(t, out, "entries:      53", "dep-49", "... and 3 more")
		mustNotContain(t, out, "dep-50")
	})
}

func TestDisallowSources(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	df := filepath.Join(t.TempDir(), "dis.txt")
	os.WriteFile(df, []byte("# c\nFile-Dep\n"), 0o644)
	r.env = append(r.env, "BBAPPROVE_DISALLOW=env-dep, other-dep", "BBAPPROVE_DISALLOW_FILE="+df)
	_, out := r.doctor(nil)
	mustContain(t, out, "config file:", "left-pad", "env-dep", "other-dep", df+"  [env, found]", "file-dep", "--disallow:", "flag only")
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
			[]string{"BBAPPROVE_TOKEN set", `item "MY_ITEM" [env] is ignored`, "BBAPPROVE_TOKEN=set (value hidden)"}, []string{"not looked up"}},
		{"name from env", validCfg, []string{"BBAPPROVE_TOKEN_NAME=MY_ITEM"}, nil, 0,
			[]string{`from 1Password item "MY_ITEM" [env] (not looked up by doctor)`}, []string{"NOT found", "BBAPPROVE_TOKEN set"}},
		{"name from file", validCfg + "user_token_name = \"FILE_ITEM\"\n", nil, nil, 0,
			[]string{`from 1Password item "FILE_ITEM" [file] (not looked up by doctor)`}, nil},
		{"name but no op", validCfg, []string{"BBAPPROVE_TOKEN_NAME=MY_ITEM"}, map[string]string{"git": "/usr/bin/git"}, 0,
			[]string{`item "MY_ITEM"`, "op (1Password CLI) NOT found on PATH"}, nil},
		{"neither", validCfg, nil, nil, 1,
			[]string{"FAIL no token: BBAPPROVE_TOKEN is not set and no 1Password item name is configured", "1 problem(s) found"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.writeXDG(tc.cfg)
			r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg} // no token source from the rig
			r.env = append(r.env, tc.env...)
			if tc.tools != nil {
				r.tools = tc.tools
			}
			code, out := r.doctor(nil)
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
		{"up to date", "v1.0.0", nil, "up to date (latest release: v1.0.0)"},
		{"available", "v0.9.0", nil, "update available: v1.0.0 (run: bbapprove --update)"},
		{"cannot check", "v1.0.0", func(f *fakeRunner) { f.releaseEr = errors.New("network down") }, "could not check: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.writeXDG(validCfg)
			if tc.mod != nil {
				tc.mod(r.run)
			}
			code, out := r.doctor(func(o *Options) { o.Version = tc.version })
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
	_, out := r.doctor(func(o *Options) { o.Offline = true })
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
	_, out := r.doctor(func(o *Options) { o.Offline = true })
	mustContain(t, out, "gh auth:      not logged in")
	// gh absent: never run
	r2 := newRig(t)
	r2.writeXDG(validCfg)
	delete(r2.tools, "gh")
	_, out = r2.doctor(func(o *Options) { o.Offline = true })
	mustContain(t, out, "gh:           not found on PATH")
	if r2.run.called("gh") {
		t.Errorf("gh run although missing: %v", r2.run.calls)
	}
}

func TestPathShadowing(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.tools["bbapprove"] = "/opt/other/bbapprove"
	code, out := r.doctor(nil)
	if code != 0 {
		t.Errorf("shadowing is only a warning, code %d", code)
	}
	mustContain(t, out, "PATH shadowing: `bbapprove` on your PATH is /opt/other/bbapprove", "all good")
	// the launcher next to the binary is fine
	r.tools["bbapprove"] = filepath.Join(r.root, "bin", "bbapprove")
	_, out = r.doctor(nil)
	mustNotContain(t, out, "shadowing")
}

func TestAdapterChecks(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	os.Chmod(filepath.Join(r.root, "adapters", "bitbucket"), 0o644)
	code, out := r.doctor(nil)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	mustContain(t, out, "not executable", "FAIL adapter ", "chmod +x")
	os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
	_, out = r.doctor(nil)
	mustContain(t, out, "does not exist", "FAIL adapter ")
	os.WriteFile(filepath.Join(r.root, "adapters", "bitbucket"), []byte("#!/bin/sh\n"), 0o755)
	code, out = r.doctor(nil)
	mustContain(t, out, "exists, executable")
	if code != 0 {
		t.Errorf("code %d", code)
	}
}

func TestEnvironmentSection(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.env = append(r.env, "BBAPPROVE_JOBS=3", "BBAPPROVE_TOKEN=zzz-secret", "EDITOR=vi")
	_, out := r.doctor(nil)
	mustContain(t, out, "BBAPPROVE_JOBS=3", "BBAPPROVE_TOKEN=set (value hidden)", "NO_COLOR=1", "EDITOR=vi", "VISUAL not set")
	mustNotContain(t, out, "zzz-secret")
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
			_, out := r.doctor(func(o *Options) { o.IsTTY = tc.tty; o.Environ = r.env })
			mustNotContain(t, out, "\033", "✓", "✗")
			mustContain(t, out, "ok   ")
		})
	}
	// colour only when the app would use it
	r := newRig(t)
	r.env = []string{"XDG_CONFIG_HOME=" + r.xdg}
	r.writeXDG(validCfg)
	_, out := r.doctor(func(o *Options) { o.IsTTY = true; o.Environ = r.env })
	mustContain(t, out, "\033[32m✓")
}

func TestExitCodeAllGood(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	code, out := r.doctor(nil)
	if code != 0 || !strings.HasSuffix(out, "all good\n") {
		t.Fatalf("code %d\n%s", code, out)
	}
}
