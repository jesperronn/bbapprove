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
	if lines(short) >= 25 || lines(short)*2 > lines(full) {
		t.Errorf("default report has %d lines, -v has %d; the default must be dense", lines(short), lines(full))
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
	mustNotContain(t, out, "shadowing")
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
		mustContain(t, out, "FAIL config   no config file found", "lookup order", "1. --config flag", "4. XDG config",
			"[not found]", "FAIL no config file found (searched", "bbapprove --init")
	})
	t.Run("explicit file missing shows the lookup order", func(t *testing.T) {
		r := newRig(t)
		r.env = append(r.env, "BBAPPROVE_CONFIG=/nonexistent/x.toml")
		code, out := r.doctor(nil)
		if code != 1 {
			t.Fatalf("code %d", code)
		}
		mustContain(t, out, "lookup order", "/nonexistent/x.toml  [NOT FOUND, but selected explicitly]")
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
			"warn PATH shadowing: `bbapprove` on your PATH is /opt/other/bbapprove",
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
		mustContain(t, out, "FAIL config "+p+": the file is unchanged from the template")
	})
}

func TestCompactFileModeNoteOnlyWithVerbose(t *testing.T) {
	r := newRig(t)
	p := r.writeXDG(validCfg)
	os.Chmod(p, 0o644)
	_, out := r.doctor(nil)
	mustNotContain(t, out, "0644", "readable by group/other")
	_, out = r.doctorV(nil)
	mustContain(t, out, "0644: readable by group/other")
}

func TestVerboseIsTheFullReport(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctorV(func(o *Options) { o.Offline = true })
	mustContain(t, out, "lookup order", "file mode:", "rows matching these entries", "tools:", "VISUAL not set", "adapter:\n",
		"on PATH:      bbapprove not found")
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
		"lookup order": "\x1b[2mlookup order",
		"fail word":    "\x1b[1;31mFAIL\x1b[0m no config file found",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", name, want, out)
		}
	}
}
