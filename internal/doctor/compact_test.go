package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoldenPlainCompactDoctor(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctor(func(o *Options) { o.Offline = true })
	checkGolden(t, "doctor_ok.golden", r.normalize(out))
}

func TestGoldenPlainCompactDoctorUpdateAvailable(t *testing.T) {
	r := goldenRig(t)
	r.run.releases = `[{"tagName":"v1.2.0","isPrerelease":false,"isDraft":false}]`
	_, out := r.doctor(nil)
	checkGolden(t, "doctor_update.golden", r.normalize(out))
}

func TestGoldenPlainCompactDoctorProblems(t *testing.T) {
	r := newRig(t)
	r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg}
	r.run.releaseEr = os.ErrDeadlineExceeded
	os.MkdirAll(filepath.Join(r.xdg, "bbapprove"), 0o755)
	os.WriteFile(r.ignorePath(), []byte("x\n"), 0o600)
	os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
	_, out := r.doctor(nil)
	checkGolden(t, "doctor_problems.golden", r.normalize(out))
}

func lines(s string) int { return strings.Count(s, "\n") }

func TestCompactIsShorterThanVerbose(t *testing.T) {
	r := goldenRig(t)
	_, short := r.doctor(func(o *Options) { o.Offline = true })
	_, full := r.doctorV(func(o *Options) { o.Offline = true })
	if lines(short) >= 25 || lines(full) <= lines(short) || lines(full) > 28 {
		t.Errorf("default report has %d lines, -v has %d; both must be dense and -v must add something", lines(short), lines(full))
	}
}

func TestCompactHeaderAndRoot(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctor(func(o *Options) { o.Offline = true })
	mustContain(t, out, "bbapprove v1.0.0 (linux/amd64, go1.test) · release archive\n",
		"binary   "+r.root+"/bin/bbapprove-go (not on PATH")
	mustNotContain(t, out, "built", "(root ")
	// a binary somewhere else than <root>/bin names the root
	_, out = r.doctor(func(o *Options) {
		o.Offline = true
		o.Executable = func() (string, error) { return "/opt/tools/bbapprove", nil }
		o.LookPath = func(n string) (string, error) {
			if n == "bbapprove" {
				return "/opt/tools/bbapprove", nil
			}
			return "/usr/bin/" + n, nil
		}
	})
	mustContain(t, out, "/opt/tools/bbapprove (root "+r.root+") (on PATH: /opt/tools/bbapprove)")
	mustNotContain(t, out, "shadowed")
}

func TestCompactOneLinePerArea(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctor(nil)
	mustContain(t, out,
		"ok   update   up to date (v1.0.0)\n",
		"ok   config   "+r.xdg+"/bbapprove/config.toml (XDG config), valid TOML\n",
		"disallow:              3 entries: foo, bar, left-pad\n",
		"ok   token    BBAPPROVE_TOKEN set\n",
		"ok   ignore   "+r.ignorePath()+", 2 entries, 0600\n",
		"     disallow env: foo, bar\n",
		"effective settings (source: flag, env, file or default):",
		"delete_source_branch:  true  [default]",
	)
	mustNotContain(t, out, "ok   adapter", "lookup order", "file mode", "rows matching these entries", "left-pad\n       lodash", "update:\n", "\n\nupdate")
}

func TestCompactTokenFromOnePassword(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg, "BBAPPROVE_TOKEN_NAME=MY_ITEM"}
	_, out := r.doctor(nil)
	mustContain(t, out, `ok   token    1Password item "MY_ITEM" [env] (not looked up by doctor)`+"\n")
	for _, c := range r.run.calls {
		if strings.HasPrefix(c, "op") {
			t.Errorf("doctor ran op: %q", c)
		}
	}
}

func TestCompactEnvLineOnlyWhenSet(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.env = []string{"XDG_CONFIG_HOME=" + r.xdg, "EDITOR=vi"}
	_, out := r.doctor(func(o *Options) { o.Environ = r.env })
	mustNotContain(t, out, " env ", "EDITOR", "NO_COLOR")
	r.env = append(r.env, "NO_COLOR=1", "BBAPPROVE_JOBS=3", "BBAPPROVE_TOKEN=zzz-secret")
	_, out = r.doctor(func(o *Options) { o.Environ = r.env })
	mustContain(t, out, "     env      BBAPPROVE_JOBS set, BBAPPROVE_TOKEN set, NO_COLOR set\n")
	mustNotContain(t, out, "zzz-secret", "=3")
}

func TestCompactDisallowLineOnlyWhenSet(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	_, out := r.doctor(nil)
	mustNotContain(t, out, "disallow env", "disallow file")
	mustContain(t, out, "disallow:              1 entry: left-pad")
	df := filepath.Join(t.TempDir(), "dis.txt")
	os.WriteFile(df, []byte("File-Dep\n"), 0o644)
	r.env = append(r.env, "BBAPPROVE_DISALLOW_FILE="+df)
	_, out = r.doctor(nil)
	mustContain(t, out, "disallow file: file-dep\n")
}

func TestCompactShowsEveryProblem(t *testing.T) {
	t.Run("missing adapter", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL bitbucket adapter "+r.root+"/adapters/bitbucket does not exist", "1 problem(s) found")
	})
	t.Run("adapter not executable", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.Chmod(filepath.Join(r.root, "adapters", "bitbucket"), 0o644)
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL bitbucket adapter ", "is not executable", "chmod +x")
	})
	t.Run("bad TOML", func(t *testing.T) {
		r := newRig(t)
		p := r.writeXDG("repository = \"x\"\nauthor_email = = broken\n")
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL config   invalid TOML in "+p+" at line 2", "1 problem(s) found")
		mustNotContain(t, out, "lookup order")
	})
	t.Run("no config found shows the lookup order", func(t *testing.T) {
		r := newRig(t)
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL config   no config file found — run: bbapprove --init", "1. --config flag     not set", "4. XDG config        not found  "+r.xdg)
		mustNotContain(t, out, "lookup order", "searched")
	})
	t.Run("explicit file missing shows the lookup order", func(t *testing.T) {
		r := newRig(t)
		r.env = append(r.env, "BBAPPROVE_CONFIG=/nonexistent/x.toml")
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL config   ", "2. BBAPPROVE_CONFIG  not found  /nonexistent/x.toml")
	})
	t.Run("no token", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg}
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL token    no token: BBAPPROVE_TOKEN is not set")
	})
	t.Run("unreadable ignore list", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg)
		os.MkdirAll(r.ignorePath(), 0o755)
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL ignore   ", "is unreadable")
	})
	t.Run("update available, path shadowing, missing op", func(t *testing.T) {
		r := newRig(t)
		r.writeXDG(validCfg + "user_token_name = \"X\"\n")
		r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg}
		r.tools["bbapprove"] = "/opt/other/bbapprove"
		delete(r.tools, "op")
		code, out := r.doctor(func(o *Options) { o.Version = "v0.9.0" })
		if code != 0 {
			t.Fatalf("warnings are not failures, code %d", code)
		}
		mustContain(t, out, "warn update   update available: v1.0.0 (run: bbapprove --update)",
			"warn PATH     shadowed: /opt/other/bbapprove is not this binary",
			"warn          op (1Password CLI) NOT found on PATH", "op not found")
	})
	t.Run("placeholder values", func(t *testing.T) {
		r := newRig(t)
		tmpl, _ := os.ReadFile("../config/bbapprove.config.template")
		p := r.writeXDG(string(tmpl))
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "FAIL config   "+p+" (XDG config), valid TOML, the file is unchanged from the template — edit the file")
	})
}

func TestCompactFileModeNoteOnlyWithVerbose(t *testing.T) {
	r := newRig(t)
	p := r.writeXDG(validCfg)
	os.Chmod(p, 0o644)
	_, out := r.doctor(nil)
	mustNotContain(t, out, "0644", "default 0600")
	_, out = r.doctorV(nil)
	mustContain(t, out, "valid TOML, mode 0644 (default 0600; no secrets, a note only)\n")
}

func TestVerboseIsTheFullReport(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctorV(func(o *Options) { o.Offline = true })
	mustContain(t, out, "     root     "+r.root+"\n", "1. --config flag     not set", ", mode 0600", "rows matching these entries",
		"     env      BBAPPROVE_JOBS=3, NO_COLOR=1, XDG_CONFIG_HOME="+r.xdg+", EDITOR=vi\n",
		"     tools    git /usr/bin/git · gh /usr/bin/gh (logged in) · op /usr/bin/op\n")
}

func TestCompactNeverRunsOpOrPrintsSecret(t *testing.T) {
	r := goldenRig(t)
	r.env = append(r.env, "BBAPPROVE_TOKEN_NAME=MY_ITEM")
	_, out := r.doctor(nil)
	mustNotContain(t, out, canary)
	for _, c := range r.run.calls {
		if strings.HasPrefix(c, "op") {
			t.Errorf("doctor ran op: %q", c)
		}
	}
}

func TestCompactColour(t *testing.T) {
	r := goldenRig(t)
	out := r.doctorTTYMode(true, false, nil)
	for name, want := range map[string]string{
		"update ok":   "\x1b[32m✓\x1b[0m    \x1b[2mupdate\x1b[0m   \x1b[32mup to date\x1b[0m (\x1b[1mv1.0.0\x1b[0m)",
		"config path": "\x1b[36m$XDG/bbapprove/config.toml\x1b[0m \x1b[2m(XDG config)\x1b[0m, valid TOML",
		"env line":    "\x1b[33mBBAPPROVE_JOBS\x1b[0m \x1b[2mset\x1b[0m",
		"source":      "\x1b[32m[file]\x1b[0m",
		"final":       "\x1b[1;32mall good\x1b[0m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", name, want, out)
		}
	}
	r = newRig(t)
	os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
	r.env = []string{"XDG_CONFIG_HOME=" + r.xdg}
	out = r.doctorTTYMode(true, false, nil)
	for name, want := range map[string]string{
		"fail marker":  "\x1b[1;31m✗\x1b[0m    \x1b[2mbitbucket\x1b[0m adapter ",
		"lookup state": "\x1b[2mnot found\x1b[0m  \x1b[36m$XDG/bbapprove/config.toml\x1b[0m",
		"fail word":    "\x1b[1;31m✗\x1b[0m    \x1b[2mconfig\x1b[0m   no config file found",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", name, want, out)
		}
	}
}

func TestVerboseNamesEachFactOnce(t *testing.T) {
	r := goldenRig(t)
	cfgPath := filepath.Join(t.TempDir(), "mine.toml")
	os.WriteFile(cfgPath, []byte(validCfg), 0o600)
	_, out := r.doctorV(func(o *Options) { o.Offline = true; o.ConfigFlag = cfgPath })
	if n := strings.Count(out, cfgPath); n != 1 {
		t.Errorf("the config path appears %d times, want once:\n%s", n, out)
	}
	if n := strings.Count(out, r.ignorePath()); n != 1 {
		t.Errorf("the ignore path appears %d times, want once:\n%s", n, out)
	}
	mustContain(t, out, "(--config flag), valid TOML, mode 0600", "1. --config flag     used\n")
	// no section headings, no adapter block, no "not set" environment lines, no second disallow listing
	mustNotContain(t, out, "adapter:\n", "state:", "in use:", "parse:", "on PATH:  ", "VISUAL", "FORCE_COLOR", "disallow (matching",
		"config file:", "not found on PATH", "latest release", "lookup order")
	// variables shown as settings sources are not repeated in the environment line
	if env := lineWith(out, "     env      "); strings.Contains(env, "BBAPPROVE_DISALLOW") || strings.Contains(env, "BBAPPROVE_TOKEN") {
		t.Errorf("env line repeats a setting: %q", env)
	}
	if n := strings.Count(out, "left-pad"); n != 1 {
		t.Errorf("a disallow entry appears %d times, want once:\n%s", n, out)
	}
}

func TestVerboseEnvironmentLineOnlyWhenSet(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg)
	r.env = []string{"XDG_CONFIG_HOME=" + r.xdg, "BBAPPROVE_TOKEN=zzz-secret"}
	_, out := r.doctorV(func(o *Options) { o.Environ = r.env })
	if got, want := lineWith(out, "     env "), "     env      XDG_CONFIG_HOME="+r.xdg; got != want {
		t.Errorf("env line %q, want %q", got, want)
	}
	r.env = []string{"BBAPPROVE_TOKEN=zzz-secret"}
	_, out = r.doctorV(func(o *Options) { o.Environ = r.env; o.ConfigFlag = r.writeXDG(validCfg) })
	mustNotContain(t, out, "     env ", "zzz-secret")
}

func TestVerboseDisallowSourcesOnTheSettingsRow(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctorV(func(o *Options) { o.Offline = true })
	mustContain(t, out, "disallow:              foo, bar, left-pad  [env, file]\n", "disallow_file:         "+r.root+"/disallow.txt  [default, not found]\n")
	_, out = r.doctor(func(o *Options) { o.Offline = true })
	mustContain(t, out, "disallow:              3 entries: foo, bar, left-pad\n", "     disallow env: foo, bar\n")
}

func TestProblemsAreInlineWithTheirFixAndSummarisedByCount(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		r := newRig(t)
		r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg}
		os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
		run := r.doctor
		if verbose {
			run = r.doctorV
		}
		code, out := run(nil)
		if code != 1 {
			t.Fatalf("verbose=%v: code %d", verbose, code)
		}
		mustContain(t, out, "FAIL config   no config file found — run: bbapprove --init",
			"FAIL bitbucket adapter "+r.root+"/adapters/bitbucket does not exist — check type",
			"FAIL token    no token: BBAPPROVE_TOKEN is not set and no 1Password item name is configured — export BBAPPROVE_TOKEN=<token>",
			"\n3 problem(s) found\n")
		if n := strings.Count(out, "no API token source") + strings.Count(out, "\nFAIL no "); n != 0 {
			t.Errorf("verbose=%v: the closing summary repeats a problem:\n%s", verbose, out)
		}
		if n := strings.Count(out, "FAIL "); n != 3 {
			t.Errorf("verbose=%v: %d FAIL lines, want 3:\n%s", verbose, n, out)
		}
	}
}

func TestVerboseShowsEveryProblemToo(t *testing.T) {
	r := newRig(t)
	r.writeXDG(validCfg + "user_token_name = \"X\"\n")
	r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg}
	r.tools["bbapprove"] = "/opt/other/bbapprove"
	delete(r.tools, "op")
	os.MkdirAll(r.ignorePath(), 0o755)
	code, out := r.doctorV(func(o *Options) { o.Version = "v0.9.0" })
	if code != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
	mustContain(t, out, "warn update   update available", "warn PATH     shadowed: /opt/other/bbapprove is not this binary",
		"warn          op (1Password CLI) NOT found on PATH", "FAIL ignore   ", "op not found")
}

// lineWith returns the first line that starts with prefix, or "".
func lineWith(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
