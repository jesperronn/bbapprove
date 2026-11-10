package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zent struct {
	name string
	body string
	mode os.FileMode // 0 = regular file
}

func zipBytes(t *testing.T, ents ...zent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.body))
	}
	zw.Close()
	return buf.Bytes()
}

const winTop = "bbapprove-v1.1.1-windows-amd64"

func goodZip(t *testing.T) []byte {
	return zipBytes(t,
		zent{name: winTop + "/", mode: os.ModeDir | 0o755},
		zent{name: winTop + "/bbapprove.exe", body: "new exe"},
		zent{name: winTop + "/bbapprove.config.template", body: "new template"},
		zent{name: winTop + "/README.md", body: "new readme"},
		zent{name: winTop + "/CHANGELOG.md", body: "new changelog"},
		zent{name: winTop + "/LICENSE", body: "new license"},
	)
}

func TestAssetNameWindows(t *testing.T) {
	cases := []struct{ goos, arch, want string }{
		{"windows", "amd64", "bbapprove-v1.1.1-windows-amd64.zip"},
		{"windows", "arm64", "bbapprove-v1.1.1-windows-amd64.zip"},
		{"linux", "amd64", "bbapprove-v1.1.1-linux-amd64.tar.gz"},
		{"darwin", "arm64", "bbapprove-v1.1.1-darwin-arm64.tar.gz"},
		{"windows", "386", ""},
		{"plan9", "amd64", ""},
	}
	for _, c := range cases {
		got, err := AssetName("v1.1.1", c.goos, c.arch)
		if c.want == "" {
			if err == nil || !strings.Contains(err.Error(), "unsupported platform") {
				t.Errorf("%s/%s: %q %v", c.goos, c.arch, got, err)
			}
		} else if err != nil || got != c.want {
			t.Errorf("%s/%s: %q %v", c.goos, c.arch, got, err)
		}
	}
}

func TestExtractZipRejects(t *testing.T) {
	big := strings.Repeat("x", 4096)
	many := make([]zent, 0, maxEntries+1)
	for i := 0; i <= maxEntries; i++ {
		many = append(many, zent{name: "top/f"})
	}
	cases := []struct {
		name string
		data []byte
		max  int64
		want string
	}{
		{"dot-dot", zipBytes(t, zent{name: "top/../../evil", body: "x"}), 1 << 20, "escapes"},
		{"leading dot-dot", zipBytes(t, zent{name: "../evil", body: "x"}), 1 << 20, "escapes"},
		{"absolute", zipBytes(t, zent{name: "/etc/passwd", body: "x"}), 1 << 20, "unsafe"},
		{"drive letter", zipBytes(t, zent{name: "C:/evil.exe", body: "x"}), 1 << 20, "unsafe"},
		{"UNC", zipBytes(t, zent{name: "//server/share/x", body: "x"}), 1 << 20, "unsafe"},
		{"backslash", zipBytes(t, zent{name: "top\\..\\x", body: "x"}), 1 << 20, "unsafe"},
		{"symlink", zipBytes(t, zent{name: "top/l", body: "/etc", mode: os.ModeSymlink | 0o777}), 1 << 20, "symlink"},
		{"file at top level", zipBytes(t, zent{name: "loose", body: "x"}), 1 << 20, "outside a top-level"},
		{"second top folder", zipBytes(t, zent{name: "top/a", body: "x"}, zent{name: "other/b", body: "x"}), 1 << 20, "outside the top"},
		{"oversize", zipBytes(t, zent{name: "top/a", body: big}, zent{name: "top/b", body: big}), 5000, "limit"},
		{"duplicate", zipBytes(t, zent{name: "top/a", body: "1"}, zent{name: "top/a", body: "2"}), 1 << 20, "exist"},
		{"too many entries", zipBytes(t, many...), 1 << 20, "too many"},
		{"empty", zipBytes(t), 1 << 20, "empty"},
		{"not a zip", []byte("hello"), 1 << 20, "not a zip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			src := filepath.Join(parent, "a.zip")
			os.WriteFile(src, c.data, 0o600)
			dest := filepath.Join(parent, "stage")
			os.Mkdir(dest, 0o700)
			_, err := ExtractZip(src, dest, c.max)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
			if ents, _ := os.ReadDir(parent); len(ents) != 2 {
				t.Errorf("stray entries next to stage: %v", ents)
			}
		})
	}
}

func TestExtractZipLayout(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "a.zip")
	os.WriteFile(src, goodZip(t), 0o600)
	dest := filepath.Join(parent, "stage")
	os.Mkdir(dest, 0o700)
	top, err := ExtractZip(src, dest, 1<<20)
	if err != nil || top != winTop {
		t.Fatalf("%q %v", top, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, top, "bbapprove.exe")); string(b) != "new exe" {
		t.Errorf("exe content %q", b)
	}
}

type winCase struct {
	t      *testing.T
	h      *harness
	dir    string // the install folder
	parent string
	tag    string
	asset  string
	data   []byte
	sums   func(asset string, data []byte) string
	before string
}

func newWin(t *testing.T) *winCase {
	t.Helper()
	wc := &winCase{t: t, tag: "v1.1.1", asset: "bbapprove-v1.1.1-windows-amd64.zip", data: goodZip(t)}
	wc.sums = func(asset string, data []byte) string {
		s := sha256.Sum256(data)
		return hex.EncodeToString(s[:]) + "  " + asset + "\n"
	}
	wc.h = newHarness(t, "v1.1.0", Archive, func(c Cmd) ([]byte, error) {
		a := strings.Join(c.Args, " ")
		switch {
		case strings.HasPrefix(a, "release list"):
			return ghList(rel{"v1.1.1", false, false}, rel{"v1.1.0", false, false}), nil
		case strings.HasPrefix(a, "release view"):
			return []byte(`{"body":"notes","url":"https://github.com/jesperronn/bbapprove/releases/tag/v1.1.1"}`), nil
		case strings.HasPrefix(a, "release download"):
			var dir string
			for i, x := range c.Args {
				if x == "-D" {
					dir = c.Args[i+1]
				}
			}
			if wc.sums != nil {
				os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(wc.sums(wc.asset, wc.data)), 0o644)
			}
			os.WriteFile(filepath.Join(dir, wc.asset), wc.data, 0o644)
			return nil, nil
		}
		return nil, errors.New("unexpected " + c.Name + " " + a)
	})
	wc.dir = filepath.Join(t.TempDir(), "bbapprove-v1.1.0-windows-amd64")
	wc.parent = filepath.Dir(wc.dir)
	os.MkdirAll(wc.dir, 0o755)
	for n, b := range map[string]string{"bbapprove.exe": "old exe", "bbapprove.config.template": "old template",
		"README.md": "old readme", "CHANGELOG.md": "old changelog", "LICENSE": "old license"} {
		os.WriteFile(filepath.Join(wc.dir, n), []byte(b), 0o644)
	}
	o := &wc.h.o
	o.GOOS, o.GOARCH, o.ExeDir, o.InstallRoot, o.Yes = "windows", "arm64", wc.dir, filepath.Join(wc.dir, "nowhere"), true
	wc.before = treeHash(t, wc.dir)
	return wc
}

func (wc *winCase) read(name string) string {
	b, err := os.ReadFile(filepath.Join(wc.dir, name))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (wc *winCase) noScratch() {
	wc.t.Helper()
	ents, _ := os.ReadDir(wc.parent)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".bbapprove-update-") {
			wc.t.Errorf("scratch dir left behind: %s", e.Name())
		}
	}
}

func TestUpdateWindowsSuccess(t *testing.T) {
	wc := newWin(t)
	if code := Update(context.Background(), wc.h.o); code != 0 {
		t.Fatalf("exit %d\nerr: %s\nout: %s", code, wc.h.errOut, wc.h.out)
	}
	want := map[string]string{"bbapprove.exe": "new exe", "bbapprove.exe.old": "old exe", "bbapprove.config.template": "new template",
		"README.md": "new readme", "CHANGELOG.md": "new changelog", "LICENSE": "new license"}
	for n, c := range want {
		if got := wc.read(n); got != c {
			t.Errorf("%s = %q, want %q", n, got, c)
		}
	}
	if ents, _ := os.ReadDir(wc.dir); len(ents) != len(want) {
		t.Errorf("unexpected files in the install folder: %v", ents)
	}
	wc.noScratch()
	out := wc.h.out.String()
	for _, w := range []string{"v1.1.0 -> v1.1.1", "bbapprove-v1.1.1-windows-amd64.zip", wc.dir,
		"the new version starts the next time you run bbapprove; bbapprove.exe.old is the previous version (delete it when you no longer need it)"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if !wc.h.r.called("gh release download v1.1.1 --repo jesperronn/bbapprove -p bbapprove-v1.1.1-windows-amd64.zip -p SHA256SUMS -D ") {
		t.Errorf("download call: %v", wc.h.r.calls)
	}
}

func TestUpdateWindowsStaleOldReplaced(t *testing.T) {
	wc := newWin(t)
	os.WriteFile(filepath.Join(wc.dir, "bbapprove.exe.old"), []byte("ancient"), 0o644)
	if code := Update(context.Background(), wc.h.o); code != 0 {
		t.Fatalf("exit %d err %s", code, wc.h.errOut)
	}
	if wc.read("bbapprove.exe") != "new exe" || wc.read("bbapprove.exe.old") != "old exe" {
		t.Errorf("exe %q old %q", wc.read("bbapprove.exe"), wc.read("bbapprove.exe.old"))
	}
}

func TestUpdateWindowsKeepsConfig(t *testing.T) {
	wc := newWin(t)
	cfg := filepath.Join(t.TempDir(), "bbapprove", "config.toml") // %AppData%\bbapprove stand-in
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte("keep"), 0o600)
	wc.h.o.PathEnv = filepath.Dir(cfg)
	if code := Update(context.Background(), wc.h.o); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "keep" {
		t.Error("config changed")
	}
}

func TestUpdateWindowsAbortsBeforeTouchingInstall(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(wc *winCase)
		want   string
	}{
		{"wrong checksum", func(wc *winCase) {
			wc.sums = func(asset string, _ []byte) string { return strings.Repeat("a", 64) + "  " + asset + "\n" }
		}, "checksum mismatch"},
		{"no SHA256SUMS entry", func(wc *winCase) {
			wc.sums = func(string, []byte) string { return strings.Repeat("a", 64) + "  other.zip\n" }
		}, "no entry for"},
		{"no SHA256SUMS", func(wc *winCase) { wc.sums = nil }, "cannot read SHA256SUMS"},
		{"zip-slip", func(wc *winCase) { wc.data = zipBytes(t, zent{name: "top/../../evil", body: "x"}) }, "refusing the archive"},
		{"absolute path", func(wc *winCase) { wc.data = zipBytes(t, zent{name: "/evil", body: "x"}) }, "refusing the archive"},
		{"drive path", func(wc *winCase) { wc.data = zipBytes(t, zent{name: "C:\\evil", body: "x"}) }, "refusing the archive"},
		{"symlink entry", func(wc *winCase) {
			wc.data = zipBytes(t, zent{name: winTop + "/bbapprove.exe", body: "x", mode: os.ModeSymlink | 0o777})
		}, "symlink"},
		{"oversize", func(wc *winCase) { wc.h.o.MaxExtract = 10 }, "limit"},
		{"no exe in the release", func(wc *winCase) {
			wc.data = zipBytes(t, zent{name: winTop + "/README.md", body: "x"})
		}, "holds no bbapprove.exe"},
		{"folder without bbapprove.exe", func(wc *winCase) { os.Remove(filepath.Join(wc.dir, "bbapprove.exe")) }, "does not look like"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wc := newWin(t)
			c.mutate(wc)
			wc.before = treeHash(t, wc.dir)
			parentBefore := dirNames(wc.parent)
			if code := Update(context.Background(), wc.h.o); code != 1 {
				t.Fatalf("exit %d, want 1; err %q", code, wc.h.errOut)
			}
			if !strings.Contains(wc.h.errOut.String(), c.want) {
				t.Errorf("stderr %q lacks %q", wc.h.errOut, c.want)
			}
			if got := treeHash(t, wc.dir); got != wc.before {
				t.Errorf("install changed\nbefore:\n%s\nafter:\n%s", wc.before, got)
			}
			if strings.Join(parentBefore, ",") != strings.Join(dirNames(wc.parent), ",") {
				t.Errorf("parent not clean: %v", dirNames(wc.parent))
			}
		})
	}
}

func dirNames(d string) []string {
	var n []string
	ents, _ := os.ReadDir(d)
	for _, e := range ents {
		n = append(n, e.Name())
	}
	return n
}

func TestUpdateWindowsRollsBack(t *testing.T) {
	// fail the Nth rename of the swap: 1 = exe aside, 2 = new exe in, 3 = template aside, 4 = template in,
	// 7 = README in, 9 = LICENSE aside
	for _, failAt := range []int{1, 2, 3, 4, 6, 9} {
		t.Run("rename "+string(rune('0'+failAt)), func(t *testing.T) {
			wc := newWin(t)
			os.WriteFile(filepath.Join(wc.dir, "bbapprove.exe.old"), []byte("stale"), 0o644)
			wc.before = treeHash(t, wc.dir)
			n := 0
			wc.h.o.rename = func(a, b string) error {
				if n++; n == failAt {
					return errors.New("access denied")
				}
				return os.Rename(a, b)
			}
			if code := Update(context.Background(), wc.h.o); code != 1 {
				t.Fatalf("exit %d out %s err %s", code, wc.h.out, wc.h.errOut)
			}
			if !strings.Contains(wc.h.errOut.String(), "access denied") || !strings.Contains(wc.h.errOut.String(), "restored") {
				t.Errorf("stderr %q", wc.h.errOut)
			}
			for _, name := range []string{"bbapprove.exe", "bbapprove.config.template", "README.md", "CHANGELOG.md", "LICENSE"} {
				if got := wc.read(name); !strings.HasPrefix(got, "old") {
					t.Errorf("%s = %q, want the old content", name, got)
				}
			}
			ents := dirNames(wc.dir)
			for _, e := range ents {
				if strings.HasSuffix(e, bakSuffix) {
					t.Errorf("backup left behind: %s", e)
				}
			}
			wc.noScratch()
		})
	}
}

func TestUpdateWindowsDeclinedChangesNothing(t *testing.T) {
	wc := newWin(t)
	wc.h.o.Yes, wc.h.o.IsTTY, wc.h.o.Stdin = false, true, strings.NewReader("n\n")
	if code := Update(context.Background(), wc.h.o); code != 1 || wc.h.r.called("gh release download") {
		t.Errorf("exit %d calls %v", code, wc.h.r.calls)
	}
	wc = newWin(t)
	wc.h.o.Yes = false // not a terminal
	if code := Update(context.Background(), wc.h.o); code != 2 || !strings.Contains(wc.h.out.String(), "Plan:") || wc.h.r.called("gh release download") {
		t.Errorf("exit %d", code)
	}
	if treeHash(t, wc.dir) != wc.before {
		t.Error("install changed")
	}
}

func TestManualStepsWindows(t *testing.T) {
	h := newHarness(t, "v1.1.0", Archive, func(Cmd) ([]byte, error) { return nil, errNoGH })
	h.o.GOOS, h.o.GOARCH, h.o.ExeDir, h.o.Yes = "windows", "amd64", `C:\Tools\bbapprove`, true
	if code := Update(context.Background(), h.o); code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{"gh release download", "Get-FileHash", "Expand-Archive", "windows-amd64.zip", "bbapprove.exe.old", "%AppData%"} {
		if !strings.Contains(h.out.String(), w) {
			t.Errorf("manual steps lack %q:\n%s", w, h.out)
		}
	}
	if strings.Contains(h.out.String(), "tar -xzf") || strings.Contains(h.out.String(), "shasum") {
		t.Errorf("Unix commands in the Windows steps:\n%s", h.out)
	}
}
