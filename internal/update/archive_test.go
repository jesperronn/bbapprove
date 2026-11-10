package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"bbapprove/internal/testfix"
)

type ent struct {
	name string
	typ  byte
	body string
	link string
	mode int64
}

func tarball(t *testing.T, ents ...ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		if e.typ == 0 {
			e.typ = tar.TypeReg
		}
		if e.mode == 0 {
			e.mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func goodTarball(t *testing.T, top string) []byte {
	return tarball(t,
		ent{name: top + "/", typ: tar.TypeDir, mode: 0o755},
		ent{name: top + "/bin/", typ: tar.TypeDir, mode: 0o755},
		ent{name: top + "/bin/bbapprove", body: "#!/bin/sh\necho new\n", mode: 0o755},
		ent{name: top + "/README.md", body: "hello"},
	)
}

func TestExtractRejectsMalicious(t *testing.T) {
	big := strings.Repeat("x", 4096)
	cases := []struct {
		name string
		data []byte
		max  int64
		want string
	}{
		{"dot-dot", tarball(t, ent{name: "top/../../evil", body: "x"}), 1 << 20, "escapes"},
		{"leading dot-dot", tarball(t, ent{name: "../evil", body: "x"}), 1 << 20, "escapes"},
		{"absolute", tarball(t, ent{name: "/etc/passwd", body: "x"}), 1 << 20, "unsafe"},
		{"backslash", tarball(t, ent{name: "top\\..\\x", body: "x"}), 1 << 20, "unsafe"},
		{"file at top level", tarball(t, ent{name: "loose", body: "x"}), 1 << 20, "outside a top-level"},
		{"second top dir", tarball(t, ent{name: "top/a", body: "x"}, ent{name: "other/b", body: "x"}), 1 << 20, "outside the top"},
		{"symlink absolute", tarball(t, ent{name: "top/l", typ: tar.TypeSymlink, link: "/etc"}), 1 << 20, "unsafe target"},
		{"symlink up", tarball(t, ent{name: "top/l", typ: tar.TypeSymlink, link: "../../x"}), 1 << 20, "outside"},
		{"symlink to top's parent", tarball(t, ent{name: "top/l", typ: tar.TypeSymlink, link: ".."}), 1 << 20, "outside"},
		{"symlink chain escape", tarball(t,
			ent{name: "top/b", typ: tar.TypeSymlink, link: "."},
			ent{name: "top/a", typ: tar.TypeSymlink, link: "b/.."}), 1 << 20, "resolves outside"},
		{"write through symlink", tarball(t,
			ent{name: "top/sub/", typ: tar.TypeDir},
			ent{name: "top/d", typ: tar.TypeSymlink, link: "sub"},
			ent{name: "top/d/x", body: "x"}), 1 << 20, "through the symlink"},
		{"hardlink outside", tarball(t, ent{name: "top/h", typ: tar.TypeLink, link: "/etc/passwd"}), 1 << 20, "hardlink"},
		{"hardlink to parent", tarball(t, ent{name: "top/h", typ: tar.TypeLink, link: "x/passwd"}), 1 << 20, "hardlink"},
		{"device", tarball(t, ent{name: "top/dev", typ: tar.TypeChar}), 1 << 20, "unsupported type"},
		{"fifo", tarball(t, ent{name: "top/f", typ: tar.TypeFifo}), 1 << 20, "unsupported type"},
		{"oversize", tarball(t, ent{name: "top/a", body: big}, ent{name: "top/b", body: big}), 5000, "limit"},
		{"duplicate", tarball(t, ent{name: "top/a", body: "1"}, ent{name: "top/a", body: "2"}), 1 << 20, "exist"},
		{"empty", tarball(t), 1 << 20, "empty"},
		{"not gzip", []byte("hello"), 1 << 20, "gzip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "stage")
			os.Mkdir(dest, 0o700)
			_, err := ExtractTarGz(bytes.NewReader(c.data), dest, c.max)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
			// nothing may land outside the staging dir
			ents, _ := os.ReadDir(parent)
			if len(ents) != 1 {
				t.Errorf("stray entries next to stage: %v", ents)
			}
		})
	}
}

func TestExtractModesAndLayout(t *testing.T) {
	testfix.RequireUnixModes(t)
	data := tarball(t,
		ent{name: "./top/", typ: tar.TypeDir, mode: 0o755},
		ent{name: "./top/bin/tool", body: "x", mode: 0o4755},  // setuid
		ent{name: "top/data", body: "d", mode: 0o2644},        // setgid
		ent{name: "top/lib/", typ: tar.TypeDir, mode: 0o1777}, // sticky
		ent{name: "top/lib/real", body: "r"},
		ent{name: "top/lib/alias", typ: tar.TypeSymlink, link: "real"},
		ent{name: "top/lib/copy", typ: tar.TypeLink, link: "top/lib/real"},
	)
	dest := t.TempDir()
	top, err := ExtractTarGz(bytes.NewReader(data), dest, 1<<20)
	if err != nil || top != "top" {
		t.Fatalf("top=%q err=%v", top, err)
	}
	for p, want := range map[string]fs.FileMode{"bin/tool": 0o755, "data": 0o644, "lib": 0o777} {
		st, err := os.Stat(filepath.Join(dest, "top", p))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want || st.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			t.Errorf("%s mode %v, want %v without special bits", p, st.Mode(), want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "top/lib/alias")); string(b) != "r" {
		t.Error("symlink inside the tree should work")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "top/lib/copy")); string(b) != "r" {
		t.Error("hardlink inside the tree should be materialised")
	}
}

func TestAssetName(t *testing.T) {
	if a, err := AssetName("v1.0.0-rc.3", "linux", "amd64"); err != nil || a != "bbapprove-v1.0.0-rc.3-linux-amd64.tar.gz" {
		t.Errorf("%q %v", a, err)
	}
	for _, p := range [][2]string{{"windows", "386"}, {"darwin", "386"}, {"freebsd", "arm64"}, {"linux", "riscv64"}} {
		if _, err := AssetName("v1", p[0], p[1]); err == nil || !strings.Contains(err.Error(), "unsupported platform") {
			t.Errorf("%v: %v", p, err)
		}
	}
}

// treeHash lists every path with mode and content hash, so before/after comparison catches any change.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		info, _ := d.Info()
		line := p + " " + info.Mode().String()
		if info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			s := sha256.Sum256(b)
			line += " " + hex.EncodeToString(s[:])
		}
		lines = append(lines, line)
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

type archiveCase struct {
	h       *harness
	parent  string
	tag     string
	asset   string
	oldHash string
	sums    func(asset string, data []byte) string
	data    []byte
	body    string
	tt      *testing.T
}

func (ac *archiveCase) t() *testing.T { return ac.tt }

// newArchive builds a harness whose fake gh serves a release with the given tarball.
func newArchive(t *testing.T) *archiveCase {
	t.Helper()
	ac := &archiveCase{tag: "v1.0.0-rc.3", body: "## Changes\n- a thing"}
	ac.asset = "bbapprove-" + ac.tag + "-darwin-arm64.tar.gz"
	ac.data = goodTarball(t, "bbapprove-"+ac.tag+"-darwin-arm64")
	ac.sums = func(asset string, data []byte) string {
		s := sha256.Sum256(data)
		return hex.EncodeToString(s[:]) + "  " + asset + "\n" + strings.Repeat("0", 64) + "  other.tar.gz\n"
	}
	ac.h = newHarness(t, "v1.0.0-rc.2", Archive, func(c Cmd) ([]byte, error) {
		a := strings.Join(c.Args, " ")
		switch {
		case strings.HasPrefix(a, "release list"):
			return ghList(rel{ac.tag, true, false}, rel{"v1.0.0-rc.2", true, false}), nil
		case strings.HasPrefix(a, "release view"):
			return []byte(`{"body":` + jsonStr(ac.body) + `,"url":"https://github.com/jesperronn/bbapprove/releases/tag/` + ac.tag + `"}`), nil
		case strings.HasPrefix(a, "release download"):
			var dir string
			for i, x := range c.Args {
				if x == "-D" {
					dir = c.Args[i+1]
				}
			}
			if ac.sums != nil {
				os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(ac.sums(ac.asset, ac.data)), 0o644)
			}
			os.WriteFile(filepath.Join(dir, ac.asset), ac.data, 0o644)
			return nil, nil
		}
		return nil, errors.New("unexpected " + c.Name + " " + a)
	})
	ac.parent = filepath.Dir(ac.h.o.InstallRoot)
	os.WriteFile(filepath.Join(ac.h.o.InstallRoot, "bin", "bbapprove"), []byte("old binary"), 0o755)
	os.WriteFile(filepath.Join(ac.h.o.InstallRoot, "bbapprove.config.toml"), []byte("repository = \"x\"\n"), 0o600)
	ac.oldHash = treeHash(t, ac.h.o.InstallRoot)
	ac.h.o.Yes = true
	return ac
}

func jsonStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\x1b", `\u001b`)
	return `"` + r.Replace(s) + `"`
}

func (ac *archiveCase) leftovers(t *testing.T) []string {
	ents, _ := os.ReadDir(ac.parent)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestUpdateArchiveSuccess(t *testing.T) {
	ac := newArchive(t)
	h := ac.h
	// a PATH entry holding a symlink into the OLD tree
	bindir := t.TempDir()
	link := filepath.Join(bindir, "bbapprove")
	if err := os.Symlink(filepath.Join(h.o.InstallRoot, "bin", "bbapprove"), link); err != nil {
		t.Fatal(err)
	}
	h.o.PathEnv = bindir + string(os.PathListSeparator) + "/nonexistent"
	ac.body = "line\x1b[31mred\n" + strings.Repeat("note\n", 40)

	if code := Update(context.Background(), h.o); code != 0 {
		t.Fatalf("exit %d\nerr: %s\nout: %s", code, h.errOut, h.out)
	}
	dest := filepath.Join(ac.parent, "bbapprove-v1.0.0-rc.3-darwin-arm64")
	if b, err := os.ReadFile(filepath.Join(dest, "bin", "bbapprove")); err != nil || !strings.Contains(string(b), "echo new") {
		t.Fatalf("new tree not installed: %v", err)
	}
	if got := treeHash(t, h.o.InstallRoot); got != ac.oldHash {
		t.Errorf("OLD TREE CHANGED\nbefore:\n%s\nafter:\n%s", ac.oldHash, got)
	}
	for _, n := range ac.leftovers(t) {
		if strings.HasPrefix(n, ".bbapprove-update-") {
			t.Errorf("scratch dir left behind: %s", n)
		}
	}
	out := h.out.String()
	for _, w := range []string{
		"v1.0.0-rc.2 -> v1.0.0-rc.3", "bbapprove-v1.0.0-rc.3-darwin-arm64.tar.gz", dest,
		"truncated", "releases/tag/v1.0.0-rc.3", "Proceed?", /* not printed with --yes */
	} {
		if w == "Proceed?" {
			if strings.Contains(out, w) {
				t.Errorf("--yes must skip the prompt")
			}
			continue
		}
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Count(out, "note") > notesLines {
		t.Errorf("release notes must be sanitised and truncated:\n%s", out)
	}
	wantSwitch := "ln -sfn " + filepath.Join(dest, "bin", "bbapprove") + " " + link
	wantBack := "ln -sfn " + filepath.Join(h.o.InstallRoot, "bin", "bbapprove") + " " + link
	for _, w := range []string{wantSwitch, wantBack, "untouched and shared", "bbapprove.config.toml: copy it", "old tree is kept"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	// the symlink itself must NOT have been switched
	if tgt, _ := os.Readlink(link); !strings.HasPrefix(tgt, h.o.InstallRoot) {
		t.Errorf("link was switched automatically: %s", tgt)
	}
	if !h.r.called("gh release download v1.0.0-rc.3 --repo jesperronn/bbapprove -p " + ac.asset + " -p SHA256SUMS -D ") {
		t.Errorf("download call: %v", h.r.calls)
	}
}

func TestUpdateArchiveNoSymlinkOnPath(t *testing.T) {
	ac := newArchive(t)
	if code := Update(context.Background(), ac.h.o); code != 0 {
		t.Fatalf("exit %d %s", code, ac.h.errOut)
	}
	out := ac.h.out.String()
	if !strings.Contains(out, "To switch: point your bbapprove command at") || !strings.Contains(out, "To roll back:") ||
		strings.Contains(out, "To switch:    ln -sfn") {
		t.Errorf("generic switch text expected:\n%s", out)
	}
}

func TestUpdateArchiveAborts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ac *archiveCase)
		want   string
	}{
		{"checksum mismatch", func(ac *archiveCase) {
			ac.sums = func(asset string, _ []byte) string { return strings.Repeat("a", 64) + "  " + asset + "\n" }
		}, "checksum mismatch"},
		{"no line for the asset", func(ac *archiveCase) {
			ac.sums = func(string, []byte) string { return strings.Repeat("a", 64) + "  other.tar.gz\n" }
		}, "no entry for"},
		{"no SHA256SUMS at all", func(ac *archiveCase) { ac.sums = nil }, "cannot read SHA256SUMS"},
		{"destination exists", func(ac *archiveCase) {
			os.Mkdir(filepath.Join(ac.parent, "bbapprove-v1.0.0-rc.3-darwin-arm64"), 0o755)
		}, "already exists"},
		{"malicious tarball", func(ac *archiveCase) {
			ac.data = tarball(ac.t(), ent{name: "top/../../evil", body: "x"})
		}, "refusing the archive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ac := newArchive(t)
			ac.tt = t
			before := ac.leftovers(t)
			c.mutate(ac)
			if c.name == "destination exists" {
				before = ac.leftovers(t)
			}
			if code := Update(context.Background(), ac.h.o); code != 1 {
				t.Fatalf("exit %d, want 1; err %q", code, ac.h.errOut)
			}
			if !strings.Contains(ac.h.errOut.String(), c.want) {
				t.Errorf("stderr %q lacks %q", ac.h.errOut, c.want)
			}
			if got := treeHash(t, ac.h.o.InstallRoot); got != ac.oldHash {
				t.Error("old tree changed")
			}
			after := ac.leftovers(t)
			if strings.Join(before, ",") != strings.Join(after, ",") {
				t.Errorf("parent dir not clean: before %v after %v", before, after)
			}
			if c.name == "destination exists" && ac.h.r.called("gh release download") {
				t.Error("must refuse before downloading")
			}
		})
	}
}

func TestUpdateArchiveUnsupportedPlatform(t *testing.T) {
	ac := newArchive(t)
	ac.h.o.GOOS = "freebsd"
	if code := Update(context.Background(), ac.h.o); code != 2 || !strings.Contains(ac.h.errOut.String(), "unsupported platform freebsd/arm64") {
		t.Errorf("exit %d err %q", code, ac.h.errOut)
	}
	if ac.h.r.called("gh release download") {
		t.Error("must not download")
	}
}

func TestUpdateArchivePromptDeclined(t *testing.T) {
	ac := newArchive(t)
	ac.h.o.Yes, ac.h.o.IsTTY, ac.h.o.Stdin = false, true, strings.NewReader("n\n")
	if code := Update(context.Background(), ac.h.o); code != 1 || ac.h.r.called("gh release download") {
		t.Errorf("exit %d calls %v", code, ac.h.r.calls)
	}
	ac = newArchive(t)
	ac.h.o.Yes = false
	if code := Update(context.Background(), ac.h.o); code != 2 || !strings.Contains(ac.h.errOut.String(), "re-run with --yes") || !strings.Contains(ac.h.out.String(), "Plan:") {
		t.Errorf("non-tty: exit %d err %q", code, ac.h.errOut)
	}
}

func TestUpdateArchiveGhMissing(t *testing.T) {
	// gh missing right away (check fails)
	h := newHarness(t, "v1.0.0-rc.2", Archive, func(Cmd) ([]byte, error) { return nil, errNoGH })
	h.o.Yes = true
	if code := Update(context.Background(), h.o); code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{"shasum -a 256 -c", "tar -xzf", "releases/latest", "gh"} {
		if !strings.Contains(h.out.String(), w) {
			t.Errorf("manual steps lack %q:\n%s", w, h.out)
		}
	}
	// gh disappears between check and view
	ac := newArchive(t)
	inner := ac.h.r.h
	ac.h.r.h = func(c Cmd) ([]byte, error) {
		if len(c.Args) > 1 && c.Args[1] == "view" {
			return nil, errNoGH
		}
		return inner(c)
	}
	if code := Update(context.Background(), ac.h.o); code != 2 || !strings.Contains(ac.h.out.String(), "bbapprove-v1.0.0-rc.3-darwin-arm64.tar.gz") {
		t.Errorf("exit %d out %q", code, ac.h.out)
	}
	if ac.h.r.called("gh release download") {
		t.Error("no download without gh")
	}
}
