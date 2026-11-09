package doctor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }

// colourEnv drops NO_COLOR/FORCE_COLOR from the rig's environment and appends extra.
func colourEnv(r *rig, extra ...string) []string {
	var env []string
	for _, kv := range r.env {
		if !strings.HasPrefix(kv, "NO_COLOR=") && !strings.HasPrefix(kv, "FORCE_COLOR=") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

func (r *rig) doctorTTY(tty bool, mod func(*Options), extra ...string) string {
	r.t.Helper()
	env := colourEnv(r, extra...)
	_, out := r.doctor(func(o *Options) {
		o.Environ, o.IsTTY = env, tty
		if mod != nil {
			mod(o)
		}
	})
	return r.normalize(out)
}

func TestColourClassesInDoctor(t *testing.T) {
	r := goldenRig(t)
	r.run.releases = `[{"tagName":"v1.2.0","isPrerelease":false,"isDraft":false}]`
	out := r.doctorTTY(true, nil, "BBAPPROVE_UPDATE_REPO=example/repo")
	for name, want := range map[string]string{
		"title version":    "bbapprove \x1b[1mv1.0.0\x1b[0m  \x1b[2m(linux/amd64, go1.test)\x1b[0m",
		"dim label":        "\x1b[2minstall:\x1b[0m",
		"path":             "\x1b[36m$ROOT\x1b[0m",
		"heading":          "\x1b[1;36mconfig:\x1b[0m",
		"ok marker":        "\x1b[32m✓\x1b[0m    ",
		"warn marker":      "\x1b[33m!\x1b[0m    ",
		"update available": "\x1b[33mupdate available: v1.2.0\x1b[0m (run: bbapprove \x1b[32m--update\x1b[0m)",
		"source file":      "\x1b[32m[file]\x1b[0m",
		"source default":   "\x1b[2m[default]\x1b[0m",
		"source env":       "\x1b[33m[env]\x1b[0m",
		"env name":         "\x1b[33mBBAPPROVE_JOBS\x1b[0m=3",
		"env not set":      "\x1b[33mVISUAL\x1b[0m \x1b[2mnot set\x1b[0m",
		"flag":             "\x1b[32m--disallow:\x1b[0m",
		"list indent":      "\x1b[2m  \x1b[0mleft-pad",
		"found, used":      "\x1b[32m[found, used]\x1b[0m",
		"final":            "\x1b[1;32mall good\x1b[0m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", name, want, out)
		}
	}
}

func TestColourUpToDateAndFailures(t *testing.T) {
	r := goldenRig(t)
	if out := r.doctorTTY(true, nil); !strings.Contains(out, "\x1b[32mup to date\x1b[0m (latest release: \x1b[1mv1.0.0\x1b[0m)") {
		t.Errorf("up to date not green:\n%s", out)
	}
	r = newRig(t)
	r.run.releaseEr = os.ErrDeadlineExceeded
	os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
	r.env = []string{"XDG_CONFIG_HOME=" + r.xdg}
	out := r.doctorTTY(true, nil)
	for name, want := range map[string]string{
		"could not check": "\x1b[31mcould not check:\x1b[0m",
		"fail marker":     "\x1b[1;31m✗\x1b[0m    ",
		"fail word":       "\x1b[1;31mFAIL\x1b[0m no config file found",
		"unset source":    "\x1b[2m[unset]\x1b[0m",
		"problem count":   "\x1b[1;31m2 problem(s) found\x1b[0m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing %q in\n%s", name, want, out)
		}
	}
}

func TestColourSwitches(t *testing.T) {
	r := goldenRig(t)
	if out := r.doctorTTY(false, nil); strings.Contains(out, "\x1b") {
		t.Errorf("non-tty without FORCE_COLOR has escapes:\n%s", out)
	}
	if out := r.doctorTTY(false, nil, "FORCE_COLOR=1"); !strings.Contains(out, "\x1b[1;32mall good\x1b[0m") {
		t.Errorf("FORCE_COLOR=1 did not colour:\n%s", out)
	}
	if out := r.doctorTTY(true, nil, "FORCE_COLOR=1", "NO_COLOR=1"); strings.Contains(out, "\x1b") || strings.Contains(out, "✓") {
		t.Errorf("NO_COLOR must win over FORCE_COLOR:\n%s", out)
	}
}

var gutter = strings.NewReplacer("✓    ", "ok   ", "✗    ", "FAIL ", "!    ", "warn ")

// plainGutter maps the coloured-mode status symbols back to the plain-mode words (only at line starts).
func plainGutter(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		for _, sym := range []string{"✓    ", "✗    ", "!    "} {
			if strings.HasPrefix(l, sym) {
				lines[i] = gutter.Replace(sym) + strings.TrimPrefix(l, sym)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func TestStrippedColourEqualsPlain(t *testing.T) {
	scenarios := map[string]func() *rig{
		"ok": func() *rig { return goldenRig(t) },
		"update": func() *rig {
			r := goldenRig(t)
			r.run.releases = `[{"tagName":"v1.2.0","isPrerelease":false,"isDraft":false}]`
			return r
		},
		"problems": func() *rig {
			r := newRig(t)
			r.run.releaseEr = os.ErrDeadlineExceeded
			os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
			r.env = []string{"XDG_CONFIG_HOME=" + r.xdg}
			return r
		},
	}
	for name, mk := range scenarios {
		t.Run(name, func(t *testing.T) {
			r := mk()
			col := r.doctorTTY(true, nil)
			plain := r.doctorTTY(false, nil)
			if got := plainGutter(stripSGR(col)); got != plain {
				t.Errorf("stripped colour differs from plain\n--- stripped ---\n%s\n--- plain ---\n%s", got, plain)
			}
		})
	}
}
