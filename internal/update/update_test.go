package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records every command and answers via h. Nothing is ever executed or fetched.
type fakeRunner struct {
	calls []string
	h     func(c Cmd) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, c Cmd) ([]byte, error) {
	f.calls = append(f.calls, c.Name+" "+strings.Join(c.Args, " "))
	return f.h(c)
}

func (f *fakeRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

var errNoGH = &exec.Error{Name: "gh", Err: exec.ErrNotFound}

type rel struct {
	tag        string
	pre, draft bool
}

func ghList(rels ...rel) []byte {
	var parts []string
	for _, r := range rels {
		parts = append(parts, fmt.Sprintf(`{"tagName":%q,"isPrerelease":%v,"isDraft":%v,"publishedAt":"2026-01-01T00:00:00Z"}`, r.tag, r.pre, r.draft))
	}
	return []byte("[" + strings.Join(parts, ",") + "]")
}

type harness struct {
	o      Options
	r      *fakeRunner
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

func newHarness(t *testing.T, version string, install string, h func(c Cmd) ([]byte, error)) *harness {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "bbapprove-"+version+"-darwin-arm64")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if install == Checkout {
		os.Mkdir(filepath.Join(root, ".git"), 0o755)
	}
	hh := &harness{r: &fakeRunner{h: h}, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	hh.o = Options{Runner: hh.r, Stdout: hh.out, Stderr: hh.errOut, GOOS: "darwin", GOARCH: "arm64",
		InstallRoot: root, Version: version, PathEnv: t.TempDir()}
	return hh
}

func TestDetectInstall(t *testing.T) {
	d := t.TempDir()
	if DetectInstall(d) != Archive {
		t.Error("no .git is an archive install")
	}
	os.Mkdir(filepath.Join(d, ".git"), 0o755)
	if DetectInstall(d) != Checkout {
		t.Error(".git dir is a checkout")
	}
	w := t.TempDir()
	os.WriteFile(filepath.Join(w, ".git"), []byte("gitdir: x"), 0o644) // worktree
	if DetectInstall(w) != Checkout {
		t.Error(".git file is a checkout")
	}
}

func TestOutdated(t *testing.T) {
	stable := []rel{{"v0.9.0", false, false}, {"v1.0.0", false, false}}
	cases := []struct {
		name, version, install string
		pre                    bool
		rels                   []rel
		code                   int
		want, notWant          []string
	}{
		{"rc behind newer rc (archive)", "v1.0.0-rc.2", Archive, false,
			[]rel{{"v1.0.0-rc.2", true, false}, {"v1.0.0-rc.3", true, false}}, 1,
			[]string{"bbapprove v1.0.0-rc.2 — latest v1.0.0-rc.3 (pre-release): update available", "bbapprove --update", "/releases/tag/v1.0.0-rc.3"}, []string{"git pull"}},
		{"rc behind newer rc (checkout)", "v1.0.0-rc.2", Checkout, false,
			[]rel{{"v1.0.0-rc.3", true, false}}, 1,
			[]string{"update available", "git pull --ff-only --tags && bin/build", "bbapprove --update"}, nil},
		{"rc.2 sees rc.10 numerically and the stable beats both", "v1.0.0-rc.2", Archive, false,
			[]rel{{"v1.0.0-rc.10", true, false}, {"v1.0.0", false, false}, {"v1.0.0-rc.9", true, false}}, 1,
			[]string{"latest v1.0.0:", "update available"}, []string{"pre-release)"}},
		{"up to date", "v1.0.0-rc.3", Archive, false, []rel{{"v1.0.0-rc.3", true, false}, {"v1.0.0-rc.2", true, false}}, 0,
			[]string{"bbapprove v1.0.0-rc.3 is up to date"}, []string{"update available"}},
		{"stable ignores pre-releases", "v1.0.0", Archive, false, append(stable, rel{"v1.1.0-rc.1", true, false}), 0,
			[]string{"is up to date"}, nil},
		{"stable with --pre sees them", "v1.0.0", Archive, true, append(stable, rel{"v1.1.0-rc.1", true, false}), 1,
			[]string{"latest v1.1.0-rc.1 (pre-release)"}, nil},
		{"stable behind stable", "v0.9.0", Archive, false, stable, 1, []string{"latest v1.0.0:"}, nil},
		{"drafts ignored", "v1.0.0", Archive, false, append(stable, rel{"v2.0.0", false, true}), 0, []string{"up to date"}, nil},
		{"v-less tags and junk tags tolerated", "v1.0.0", Archive, false,
			[]rel{{"nightly", false, false}, {"2.0.0", false, false}}, 1, []string{"latest 2.0.0:"}, nil},
		{"dev build behind", "v1.0.0-rc.2-4-gabc1234", Archive, false, []rel{{"v1.0.0-rc.3", true, false}}, 1,
			[]string{"development build (base v1.0.0-rc.2, 4 commits ahead)", "update available"}, nil},
		{"dev build ahead of latest tag", "v1.0.0-rc.3-4-gabc1234-dirty", Archive, false, []rel{{"v1.0.0-rc.3", true, false}}, 0,
			[]string{"development build", "uncommitted changes", "up to date"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.version, c.install, func(Cmd) ([]byte, error) { return ghList(c.rels...), nil })
			h.o.Pre = c.pre
			if code := Outdated(context.Background(), h.o); code != c.code {
				t.Fatalf("exit %d, want %d; out=%q err=%q", code, c.code, h.out, h.errOut)
			}
			for _, w := range c.want {
				if !strings.Contains(h.out.String(), w) {
					t.Errorf("output missing %q:\n%s", w, h.out)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(h.out.String(), w) {
					t.Errorf("output must not contain %q:\n%s", w, h.out)
				}
			}
			if !h.r.called("gh release list --repo jesperronn/bbapprove --limit 30 --json tagName,isPrerelease,isDraft,publishedAt") {
				t.Errorf("unexpected calls: %v", h.r.calls)
			}
			if strings.Contains(h.out.String(), "\x1b") {
				t.Error("output must be plain")
			}
		})
	}
}

func TestOutdatedSlugAndErrors(t *testing.T) {
	h := newHarness(t, "v1.0.0", Archive, func(Cmd) ([]byte, error) { return ghList(rel{"v1.0.0", false, false}), nil })
	h.o.Slug = "acme/fork"
	Outdated(context.Background(), h.o)
	if !h.r.called("gh release list --repo acme/fork") {
		t.Errorf("custom slug not used: %v", h.r.calls)
	}
	h = newHarness(t, "v1.0.0", Archive, nil)
	h.o.Slug = "--evil"
	if code := Outdated(context.Background(), h.o); code != 2 || len(h.r.calls) != 0 {
		t.Errorf("bad slug: code %d calls %v", code, h.r.calls)
	}
	// dev: cannot compare
	h = newHarness(t, "dev", Archive, func(Cmd) ([]byte, error) { return ghList(rel{"v1.0.0", false, false}), nil })
	if code := Outdated(context.Background(), h.o); code != 2 || !strings.Contains(h.errOut.String(), "not a release") {
		t.Errorf("dev: code %d err %q", code, h.errOut)
	}
	// no releases
	h = newHarness(t, "v1.0.0", Archive, func(Cmd) ([]byte, error) { return ghList(rel{"v2.0.0", false, true}), nil })
	if code := Outdated(context.Background(), h.o); code != 2 || !strings.Contains(h.errOut.String(), "no stable releases") {
		t.Errorf("none: code %d err %q", code, h.errOut)
	}
}

func TestGhMissingFallback(t *testing.T) {
	lsRemote := "aaa\trefs/tags/v1.0.0-rc.2\nbbb\trefs/tags/v1.0.0-rc.10\nccc\trefs/tags/not-semver\nddd\trefs/tags/v0.9.0\n"
	fn := func(c Cmd) ([]byte, error) {
		if c.Name == "gh" {
			return nil, errNoGH
		}
		if c.Name == "git" && strings.HasPrefix(strings.Join(c.Args, " "), "ls-remote --tags --refs origin") {
			return []byte(lsRemote), nil
		}
		return nil, errors.New("unexpected " + c.Name)
	}
	h := newHarness(t, "v1.0.0-rc.2", Checkout, fn)
	if code := Outdated(context.Background(), h.o); code != 1 || !strings.Contains(h.out.String(), "latest v1.0.0-rc.10 (pre-release)") {
		t.Fatalf("checkout fallback: code %d out %q err %q", code, h.out, h.errOut)
	}
	if !h.r.called("git ls-remote --tags --refs origin") {
		t.Error("ls-remote not used")
	}
	// a stable build must not be offered the rc tags by the fallback
	h = newHarness(t, "v0.9.0", Checkout, fn)
	if code := Outdated(context.Background(), h.o); code != 0 {
		t.Errorf("stable via tags: code %d out %q", code, h.out)
	}

	// archive: no fallback, exit 2 naming gh and the releases URL, git never called
	h = newHarness(t, "v1.0.0-rc.2", Archive, fn)
	code := Outdated(context.Background(), h.o)
	if code != 2 || h.r.called("git") || !strings.Contains(h.errOut.String(), "gh") ||
		!strings.Contains(h.errOut.String(), "https://github.com/jesperronn/bbapprove/releases") {
		t.Errorf("archive: code %d calls %v err %q", code, h.r.calls, h.errOut)
	}
	// checkout where both fail
	h = newHarness(t, "v1.0.0", Checkout, func(Cmd) ([]byte, error) { return nil, errors.New("network down") })
	if code := Outdated(context.Background(), h.o); code != 2 || !strings.Contains(h.errOut.String(), "network down") {
		t.Errorf("both fail: code %d err %q", code, h.errOut)
	}
}

func TestGhNotAuthenticatedFallsBackInCheckout(t *testing.T) {
	h := newHarness(t, "v1.0.0", Checkout, func(c Cmd) ([]byte, error) {
		if c.Name == "gh" {
			return nil, errors.New("exit status 4: To get started with GitHub CLI, please run: gh auth login")
		}
		return []byte("a\trefs/tags/v1.1.0\n"), nil
	})
	if code := Outdated(context.Background(), h.o); code != 1 || !strings.Contains(h.out.String(), "latest v1.1.0") {
		t.Errorf("code %d out %q", code, h.out)
	}
}

// --- --update: checkout ---

func checkoutHandler(status, branch string, ffOK bool) func(Cmd) ([]byte, error) {
	return func(c Cmd) ([]byte, error) {
		a := strings.Join(c.Args, " ")
		switch {
		case c.Name == "gh":
			return ghList(rel{"v1.1.0", false, false}), nil
		case a == "status --porcelain --untracked-files=no":
			return []byte(status), nil
		case a == "rev-parse --abbrev-ref HEAD":
			return []byte(branch + "\n"), nil
		case strings.HasPrefix(a, "merge-base --is-ancestor"):
			if !ffOK {
				return nil, errors.New("exit status 1")
			}
		case strings.HasSuffix(filepath.ToSlash(c.Name), "bin/bbapprove"):
			return []byte("bbapprove v1.1.0\n"), nil
		}
		return nil, nil
	}
}

func mutating(r *fakeRunner) bool {
	for _, c := range r.calls {
		if strings.HasPrefix(c, "git fetch") || strings.HasPrefix(c, "git pull") || strings.Contains(c, "bin/build") {
			return true
		}
	}
	return false
}

func TestUpdateCheckoutPreconditions(t *testing.T) {
	cases := []struct {
		name, status, branch string
		ff                   bool
		wantErr              string
		fetched              bool
	}{
		{"dirty", " M README.md\n", "main", true, "uncommitted changes", false},
		{"not main", "", "feature", true, `not "main"`, false},
		{"not fast-forwardable", "", "main", false, "cannot be fast-forwarded", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "v1.0.0", Checkout, checkoutHandler(c.status, c.branch, c.ff))
			h.o.Yes = true
			if code := Update(context.Background(), h.o); code != 1 {
				t.Fatalf("exit %d, want 1; err %q", code, h.errOut)
			}
			if !strings.Contains(h.errOut.String(), c.wantErr) {
				t.Errorf("stderr %q lacks %q", h.errOut, c.wantErr)
			}
			if h.r.called("git pull") || h.r.called(filepath.Join(h.o.InstallRoot, "bin", "build")) {
				t.Errorf("must not pull or build: %v", h.r.calls)
			}
			if !c.fetched && mutating(h.r) {
				t.Errorf("repo must be untouched, calls: %v", h.r.calls)
			}
		})
	}
}

func TestUpdateCheckoutSuccess(t *testing.T) {
	h := newHarness(t, "v1.0.0", Checkout, checkoutHandler("", "main", true))
	h.o.Yes = true
	if code := Update(context.Background(), h.o); code != 0 {
		t.Fatalf("exit %d err %q out %q", code, h.errOut, h.out)
	}
	want := []string{"git fetch --tags", "git merge-base --is-ancestor HEAD @{upstream}", "git pull --ff-only", filepath.Join(h.o.InstallRoot, "bin", "build")}
	var got []string
	for _, c := range h.r.calls {
		for _, w := range want {
			if strings.HasPrefix(strings.TrimSpace(c), w) {
				got = append(got, w)
			}
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("order %v, want %v (all: %v)", got, want, h.r.calls)
	}
	if !strings.Contains(h.out.String(), "Plan:") || !strings.Contains(h.out.String(), "Updated: bbapprove v1.1.0") {
		t.Errorf("out: %s", h.out)
	}
}

func TestConfirmation(t *testing.T) {
	cases := []struct {
		name, stdin string
		tty, yes    bool
		code        int
		applied     bool
		errHas      string
	}{
		{"y", "y\n", true, false, 0, true, ""},
		{"YES", "YES\n", true, false, 0, true, ""},
		{"n", "n\n", true, false, 1, false, ""},
		{"enter is no", "\n", true, false, 1, false, ""},
		{"EOF is no", "", true, false, 1, false, ""},
		{"garbage is no", "sure\n", true, false, 1, false, ""},
		{"non-tty needs --yes", "y\n", false, false, 2, false, "re-run with --yes to apply"},
		{"--yes skips prompt", "", false, true, 0, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "v1.0.0", Checkout, checkoutHandler("", "main", true))
			h.o.Stdin, h.o.IsTTY, h.o.Yes = strings.NewReader(c.stdin), c.tty, c.yes
			if code := Update(context.Background(), h.o); code != c.code {
				t.Fatalf("exit %d, want %d; err %q", code, c.code, h.errOut)
			}
			if mutating(h.r) != c.applied {
				t.Errorf("applied=%v, calls %v", mutating(h.r), h.r.calls)
			}
			if c.errHas != "" && !strings.Contains(h.errOut.String(), c.errHas) {
				t.Errorf("stderr %q lacks %q", h.errOut, c.errHas)
			}
			if !strings.Contains(h.out.String(), "Plan:") {
				t.Errorf("plan not printed: %s", h.out)
			}
			if c.tty && !c.yes && !strings.Contains(h.out.String(), "Proceed? [y/N]") {
				t.Errorf("no prompt: %s", h.out)
			}
		})
	}
}

func TestUpdateUpToDateDoesNothing(t *testing.T) {
	h := newHarness(t, "v1.1.0", Checkout, checkoutHandler("", "main", true))
	h.o.Yes = true
	if code := Update(context.Background(), h.o); code != 0 || mutating(h.r) || !strings.Contains(h.out.String(), "is up to date") {
		t.Errorf("code %d calls %v out %q", code, h.r.calls, h.out)
	}
}

func TestUpdateCheckFailureExits2(t *testing.T) {
	h := newHarness(t, "v1.0.0", Checkout, func(Cmd) ([]byte, error) { return nil, errors.New("boom") })
	h.o.Yes = true
	if code := Update(context.Background(), h.o); code != 2 {
		t.Errorf("exit %d", code)
	}
}
