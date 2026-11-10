package ignore

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"bbapprove/internal/testfix"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestDefaultPath(t *testing.T) {
	t.Run("XDG_CONFIG_HOME set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/xdg/cfg")
		want := filepath.Join("/xdg/cfg", "bbapprove", "ignore")
		if got := DefaultPath(); got != want {
			t.Errorf("DefaultPath() = %q, want %q", got, want)
		}
	})

	t.Run("XDG_CONFIG_HOME unset falls back to home", func(t *testing.T) {
		home := t.TempDir()
		testfix.SetHome(t, home)
		want := filepath.Join(home, ".config", "bbapprove", "ignore")
		if got := DefaultPath(); got != want {
			t.Errorf("DefaultPath() = %q, want %q", got, want)
		}
	})
}

func TestDependencyFromTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
		ok    bool
	}{
		{"scoped npm package", "chore(deps): update dependency @biomejs/biome to v2.6.5 (master)", "@biomejs/biome", true},
		{"maven coordinates", "fix(deps): update dependency org.jsoup:jsoup to v1.24.1", "org.jsoup:jsoup", true},
		{"property with branch", "chore(deps): update some-library.version to v26.6.7.redhat-00003 (master)", "some-library.version", true},
		{"docker digest", "chore(deps): update postgres docker digest to 123b179 (master)", "postgres", true},
		{"plain npm", "chore(deps): update dependency npm to v12.2.2", "npm", true},
		{"github action", "chore(deps): update actions/checkout action to v4", "actions/checkout", true},
		{"docker tag", "chore(deps): update docker.io/library/node docker tag to v22", "docker.io/library/node", true},
		{"bump from to", "chore(deps): bump lodash from 4.17.20 to 4.17.21", "lodash", true},
		{"uppercase prefix", "Chore(deps): Update dependency left-pad to v1.3.0", "left-pad", true},
		{"non-deps conventional title", "feat: something", "", false},
		{"non-deps scope", "feat(api): update foo to bar", "", false},
		{"empty", "", "", false},
		{"whitespace only", "   \t ", "", false},
		{"update with no name", "chore(deps): update dependency  to v1", "", false},
		{"truncated update", "chore(deps): update", "", false},
		{"garbage", "((( ]]] \x00 %%%", "", false},
		{"unicode", "chore(deps): update dependency ünï to v1", "ünï", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DependencyFromTitle(tt.title)
			if got != tt.want || ok != tt.ok {
				t.Errorf("DependencyFromTitle(%q) = (%q, %v), want (%q, %v)", tt.title, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "comments blanks duplicates and case",
			content: "# header\n\n  Foo  \nbar\nFOO\n#bar\n   \nBaz\r\n",
			want:    []string{"foo", "bar", "baz"},
		},
		{
			name:    "empty file",
			content: "",
			want:    []string{},
		},
		{
			name:    "only comments",
			content: "# a\n# b\n",
			want:    []string{},
		},
		{
			name:    "order preserved",
			content: "zeta\nalpha\nmid\n",
			want:    []string{"zeta", "alpha", "mid"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ignore")
			writeFile(t, path, tt.content)
			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "ignore")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load on missing file returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load on missing file = %#v, want empty", got)
	}
}

func TestAdd(t *testing.T) {
	t.Run("creates dirs and file with restricted perms", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nested", "deep", "ignore")
		if err := Add(path, "  Lodash "); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if got := readFile(t, path); got != "lodash\n" {
			t.Errorf("content = %q, want %q", got, "lodash\n")
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
			t.Errorf("file perm = %o, want 600", perm)
		}
		di, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if perm := di.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o700 {
			t.Errorf("dir perm = %o, want 700", perm)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		if err := Add(path, "npm"); err != nil {
			t.Fatal(err)
		}
		if err := Add(path, "NPM"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != "npm\n" {
			t.Errorf("content = %q, want %q", got, "npm\n")
		}
	})

	t.Run("appends and keeps comments and ordering", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		writeFile(t, path, "# mine\nzeta\n\n# more\n")
		if err := Add(path, "alpha"); err != nil {
			t.Fatal(err)
		}
		want := "# mine\nzeta\n\n# more\nalpha\n"
		if got := readFile(t, path); got != want {
			t.Errorf("content = %q, want %q", got, want)
		}
	})

	t.Run("adds newline when file lacks trailing newline", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		writeFile(t, path, "zeta")
		if err := Add(path, "alpha"); err != nil {
			t.Fatal(err)
		}
		if got, want := readFile(t, path), "zeta\nalpha\n"; got != want {
			t.Errorf("content = %q, want %q", got, want)
		}
	})

	t.Run("existing entry in other case leaves file untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		orig := "# c\nFoo\n"
		writeFile(t, path, orig)
		if err := Add(path, "foo"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != orig {
			t.Errorf("content = %q, want %q", got, orig)
		}
	})

	t.Run("rejects empty and multi-line names", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		for _, dep := range []string{"", "   ", "a\nb"} {
			if err := Add(path, dep); err == nil {
				t.Errorf("Add(%q) returned nil error, want error", dep)
			}
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file created despite rejected input (stat err=%v)", err)
		}
	})
}

func TestRemove(t *testing.T) {
	t.Run("preserves comments and other lines", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		writeFile(t, path, "# comment\nfoo\n\nBar\n# keep Foo\nbaz")
		if err := Remove(path, "FOO"); err != nil {
			t.Fatal(err)
		}
		want := "# comment\n\nBar\n# keep Foo\nbaz"
		if got := readFile(t, path); got != want {
			t.Errorf("content = %q, want %q", got, want)
		}
	})

	t.Run("removes every duplicate", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		writeFile(t, path, "x\n  X  \ny\n")
		if err := Remove(path, "x"); err != nil {
			t.Fatal(err)
		}
		if got, want := readFile(t, path), "y\n"; got != want {
			t.Errorf("content = %q, want %q", got, want)
		}
	})

	t.Run("missing entry leaves file unchanged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		orig := "# c\n\nkeep\n"
		writeFile(t, path, orig)
		if err := Remove(path, "absent"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != orig {
			t.Errorf("content = %q, want %q", got, orig)
		}
	})

	t.Run("comment text is never an entry", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ignore")
		orig := "# foo\n"
		writeFile(t, path, orig)
		if err := Remove(path, "foo"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != orig {
			t.Errorf("content = %q, want %q", got, orig)
		}
	})

	t.Run("missing file is not an error and not created", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nope", "ignore")
		if err := Remove(path, "foo"); err != nil {
			t.Fatalf("Remove on missing file: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("Remove created file (stat err=%v)", err)
		}
	})
}

func TestAddRemoveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ignore")
	if err := Add(path, "one"); err != nil {
		t.Fatal(err)
	}
	if err := Add(path, "two"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after Add: Load() = %#v, want %#v", got, want)
	}
	if err := Remove(path, "one"); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after Remove: Load() = %#v, want %#v", got, want)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ignore")
	if err := Add(path, "a"); err != nil {
		t.Fatal(err)
	}
	if err := Add(path, "b"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path, "a"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"ignore"}; !reflect.DeepEqual(names, want) {
		t.Errorf("dir entries = %v, want %v", names, want)
	}
	if strings.TrimSpace(readFile(t, path)) != "b" {
		t.Errorf("content = %q, want \"b\"", readFile(t, path))
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		title   string
		want    string
		ok      bool
	}{
		{"substring match", []string{"lodash"}, "chore(deps): bump lodash from 4 to 5", "lodash", true},
		{"case insensitive title", []string{"biome"}, "chore(deps): update dependency @biomejs/Biome to v2", "biome", true},
		{"case insensitive entry", []string{"Some-Library"}, "chore(deps): update some-library.version to v26", "Some-Library", true},
		{"first match wins", []string{"npm", "lodash"}, "bump lodash and npm", "npm", true},
		{"no match", []string{"lodash"}, "feat: something", "", false},
		{"empty list", nil, "anything", "", false},
		{"blank entries never match", []string{"", "  "}, "anything", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Matches(tt.entries, tt.title)
			if got != tt.want || ok != tt.ok {
				t.Errorf("Matches(%#v, %q) = (%q, %v), want (%q, %v)", tt.entries, tt.title, got, ok, tt.want, tt.ok)
			}
		})
	}
}
