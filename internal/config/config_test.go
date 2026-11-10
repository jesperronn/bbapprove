package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bbapprove/internal/style"
)

// sandbox is a fake repo root plus a fake XDG config home, both temp dirs.
type sandbox struct{ repo, xdg string }

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "") // never see the real per-user config
	return sandbox{repo: t.TempDir(), xdg: t.TempDir()}
}

func (s sandbox) writeRepo(t *testing.T, body string) string {
	t.Helper()
	return write(t, filepath.Join(s.repo, ConfigName), body)
}

func (s sandbox) writeXDG(t *testing.T, body string) string {
	t.Helper()
	return write(t, filepath.Join(s.xdg, "bbapprove", "config.toml"), body)
}

func write(t *testing.T, p, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (s sandbox) input(env map[string]string) Input {
	e := map[string]string{"XDG_CONFIG_HOME": s.xdg}
	for k, v := range env {
		e[k] = v
	}
	return Input{Env: func(k string) string { return e[k] }, RepoDir: s.repo}
}

const fileA = `type = "bitbucket"
repository = "https://file.example.com"
user_token_name = "FILE_ITEM"
author_email = "file-bot@example.com"
disallow = ["Some-Library", "another-library"]
`

func TestFileLookupOrder(t *testing.T) {
	s := newSandbox(t)
	xdg := s.writeXDG(t, `repository = "https://xdg.example.com"`)
	explicit := write(t, filepath.Join(t.TempDir(), "x.toml"), `repository = "https://explicit.example.com"`)
	envFile := write(t, filepath.Join(t.TempDir(), "e.toml"), `repository = "https://envfile.example.com"`)

	// only the XDG file exists
	c, err := Load(s.input(nil))
	if err != nil || c.File != xdg || c.Repository.Value != "https://xdg.example.com" {
		t.Fatalf("xdg: %+v %v", c, err)
	}
	// repo file beats XDG
	repoF := s.writeRepo(t, `repository = "https://repo.example.com"`)
	if c, err = Load(s.input(nil)); err != nil || c.File != repoF || c.Repository.Value != "https://repo.example.com" {
		t.Fatalf("repo: %+v %v", c, err)
	}
	// BBAPPROVE_CONFIG beats both
	in := s.input(map[string]string{"BBAPPROVE_CONFIG": envFile})
	if c, err = Load(in); err != nil || c.File != envFile || c.Repository.Value != "https://envfile.example.com" {
		t.Fatalf("env: %+v %v", c, err)
	}
	// --config beats BBAPPROVE_CONFIG
	in.ConfigFlag = explicit
	if c, err = Load(in); err != nil || c.File != explicit || c.Repository.Value != "https://explicit.example.com" {
		t.Fatalf("flag: %+v %v", c, err)
	}
}

func TestSearchedPathsListed(t *testing.T) {
	s := newSandbox(t)
	c, err := Load(s.input(nil))
	if err != nil || c.File != "" || len(c.Searched) != 2 ||
		c.Searched[0] != filepath.Join(s.repo, ConfigName) || c.Searched[1] != filepath.Join(s.xdg, "bbapprove", "config.toml") {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestExplicitConfigMissingIsError(t *testing.T) {
	s := newSandbox(t)
	s.writeRepo(t, fileA) // must NOT be used as a fallback
	missing := filepath.Join(t.TempDir(), "nope.toml")
	for name, in := range map[string]Input{
		"env":  s.input(map[string]string{"BBAPPROVE_CONFIG": missing}),
		"flag": func() Input { i := s.input(nil); i.ConfigFlag = missing; return i }(),
	} {
		_, err := Load(in)
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "no such file") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		env    map[string]string
		flag   string
		repo   Value
		token  Value
		author Value
	}{
		{"defaults only", "", nil, "", val("", FromNone), val("", FromNone), val("", FromNone)},
		{"file", fileA, nil, "", val("https://file.example.com", FromFile), val("FILE_ITEM", FromFile), val("file-bot@example.com", FromFile)},
		{"env beats file", fileA, map[string]string{
			"BBAPPROVE_REPOSITORY": "https://env.example.com", "BBAPPROVE_TOKEN_NAME": "ENV_ITEM", "BBAPPROVE_AUTHOR_EMAIL": "env-bot@example.com",
		}, "", val("https://env.example.com", FromEnv), val("ENV_ITEM", FromEnv), val("env-bot@example.com", FromEnv)},
		{"flag beats env and file", fileA, map[string]string{"BBAPPROVE_TOKEN_NAME": "ENV_ITEM"}, "FLAG_ITEM",
			val("https://file.example.com", FromFile), val("FLAG_ITEM", FromFlag), val("file-bot@example.com", FromFile)},
		{"env only", "", map[string]string{"BBAPPROVE_REPOSITORY": "https://env.example.com"}, "",
			val("https://env.example.com", FromEnv), val("", FromNone), val("", FromNone)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			if tc.file != "" {
				s.writeRepo(t, tc.file)
			}
			in := s.input(tc.env)
			in.TokenNameFlag = tc.flag
			c, err := Load(in)
			if err != nil {
				t.Fatal(err)
			}
			if c.Repository != tc.repo || c.TokenName != tc.token || c.AuthorEmail != tc.author {
				t.Errorf("repo %+v token %+v author %+v", c.Repository, c.TokenName, c.AuthorEmail)
			}
		})
	}
}

func val(v, src string) Value { return Value{v, src} }

func TestTypeDefaultAndAdapterPath(t *testing.T) {
	s := newSandbox(t)
	c, err := Load(s.input(nil))
	if err != nil || c.Type.Value != "bitbucket" || c.Type.Source != FromDefault ||
		c.Adapter.Value != filepath.Join(s.repo, "adapters", "bitbucket") {
		t.Fatalf("%+v %v", c, err)
	}
	s.writeRepo(t, `type = "gitea"`)
	if c, err = Load(s.input(nil)); err != nil || c.Type.Source != FromFile || c.Adapter.Value != filepath.Join(s.repo, "adapters", "gitea") {
		t.Fatalf("%+v %v", c, err)
	}
	// BBAPPROVE_ADAPTER wins over the type
	if c, err = Load(s.input(map[string]string{"BBAPPROVE_ADAPTER": "/x/adapter"})); err != nil || c.Adapter != val("/x/adapter", FromEnv) {
		t.Fatalf("%+v %v", c, err)
	}
	// a type is a name, never a path
	s.writeRepo(t, `type = "../evil"`)
	if _, err = Load(s.input(nil)); err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("%v", err)
	}
}

func TestMissingRequiredSettings(t *testing.T) {
	tests := []struct {
		name, file string
		env        map[string]string
		wantName   string // "" = no error
	}{
		{"nothing", "", nil, `"repository"`},
		{"repo only", `repository = "https://h.example.com"`, nil, `"author_email"`},
		{"author only", `author_email = "b@example.com"`, nil, `"repository"`},
		{"both from env", "", map[string]string{"BBAPPROVE_REPOSITORY": "https://h", "BBAPPROVE_AUTHOR_EMAIL": "b@example.com"}, ""},
		{"both in file", fileA, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			if tc.file != "" {
				s.writeRepo(t, tc.file)
			}
			c, err := Load(s.input(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			err = c.Missing()
			if tc.wantName == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range []string{tc.wantName, "cp bbapprove.config.template bbapprove.config.toml", "Searched: " + filepath.Join(s.repo, ConfigName)} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("message lacks %q:\n%v", w, err)
				}
			}
		})
	}
}

func TestDisallowMerge(t *testing.T) {
	s := newSandbox(t)
	s.writeRepo(t, fileA)
	dfile := write(t, filepath.Join(t.TempDir(), "d.txt"), "# c\nfrom-file\n")
	in := s.input(map[string]string{"BBAPPROVE_DISALLOW": "from-env", "BBAPPROVE_DISALLOW_FILE": dfile})
	c, err := Load(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Disallow, ","); got != "from-file,from-env,some-library,another-library" {
		t.Errorf("env: %s", got)
	}
	in.DisallowFlag = "from-flag" // the flag replaces the env var, still merged with file and config
	if c, err = Load(in); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Disallow, ","); got != "from-file,from-flag,some-library,another-library" {
		t.Errorf("flag: %s", got)
	}
}

func TestUnknownKeyWarns(t *testing.T) {
	s := newSandbox(t)
	s.writeRepo(t, fileA+"autor_email = \"typo\"\n")
	c, err := Load(s.input(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], `unknown key "autor_email"`) {
		t.Errorf("%v", c.Warnings)
	}
	if c.AuthorEmail.Value != "file-bot@example.com" {
		t.Error("known keys must still apply")
	}
}

func TestMalformedTOML(t *testing.T) {
	s := newSandbox(t)
	p := s.writeRepo(t, "type = \"bitbucket\"\nrepository = \n")
	_, err := Load(s.input(nil))
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want path and line info: %v", err)
	}
	s.writeRepo(t, `disallow = "not-a-list"`)
	if _, err = Load(s.input(nil)); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("wrong type: %v", err)
	}
}

func TestTemplateIsValid(t *testing.T) {
	s := newSandbox(t)
	b, err := os.ReadFile(filepath.Join("..", "..", TemplateName))
	if err != nil {
		t.Fatal(err)
	}
	s.writeRepo(t, string(b))
	c, err := Load(s.input(nil))
	if err != nil || len(c.Warnings) != 0 || c.Missing() != nil {
		t.Fatalf("%+v %v", c, err)
	}
	if c.Repository.Value != "https://bitbucket.example.com" || c.AuthorEmail.Value != "renovate-bot@example.com" ||
		strings.Join(c.Disallow, ",") != "some-library,another-library" {
		t.Errorf("%+v", c)
	}
}

func TestChildEnvAndDescribe(t *testing.T) {
	s := newSandbox(t)
	s.writeRepo(t, fileA)
	c, err := Load(s.input(map[string]string{"BBAPPROVE_TOKEN": "s3cret"}))
	if err != nil {
		t.Fatal(err)
	}
	env := c.ChildEnv([]string{"A=1", "BBAPPROVE_BASE_URL=stale"})
	if got := env[len(env)-3:]; got[0] != "BBAPPROVE_BASE_URL=https://file.example.com" || got[1] != "BBAPPROVE_TOKEN_NAME=FILE_ITEM" || got[2] != "BBAPPROVE_DELETE_SOURCE_BRANCH=true" {
		t.Errorf("%v", env)
	}
	d := c.Describe()
	for _, w := range []string{"repository:            https://file.example.com  [file]", "user_token_name:       FILE_ITEM  [file]", "delete_source_branch:  true  [default]", "[default]"} {
		if !strings.Contains(d, w) {
			t.Errorf("describe lacks %q:\n%s", w, d)
		}
	}
	if strings.Contains(d, "s3cret") {
		t.Error("describe leaked the token")
	}
}

func TestUpdateRepoKey(t *testing.T) {
	s := newSandbox(t)
	c, err := Load(s.input(nil))
	if err != nil || c.UpdateRepo != (Value{DefaultUpdateRepo, FromDefault}) {
		t.Fatalf("default: %+v %v", c.UpdateRepo, err)
	}
	s.writeRepo(t, fileA+"update_repo = \"acme/fork\"\n")
	c, err = Load(s.input(nil))
	if err != nil || c.UpdateRepo != (Value{"acme/fork", FromFile}) || len(c.Warnings) != 0 {
		t.Fatalf("file: %+v warnings %v err %v", c.UpdateRepo, c.Warnings, err)
	}
	c, _ = Load(s.input(map[string]string{"BBAPPROVE_UPDATE_REPO": "env/repo"}))
	if c.UpdateRepo != (Value{"env/repo", FromEnv}) {
		t.Errorf("env: %+v", c.UpdateRepo)
	}
	if d := c.Describe(); !strings.Contains(d, "update_repo:           env/repo  [env]") {
		t.Errorf("Describe lacks update_repo:\n%s", d)
	}
}

func TestSettingsThemed(t *testing.T) {
	c := &Config{
		Type: Value{"bitbucket", FromDefault}, Adapter: Value{"/a/b", FromFlag}, Repository: Value{"https://x.example.com", FromFile},
		AuthorEmail: Value{"b@example.com", FromEnv}, UpdateRepo: Value{"", FromNone}, DisallowFile: Value{"/d.txt", FromDefault},
		Disallow: []string{"a", "b"},
	}
	plain := c.Settings()
	if c.SettingsThemed(style.Theme{}) != plain {
		t.Fatal("zero theme must equal Settings()")
	}
	col := c.SettingsThemed(style.New(true))
	for _, want := range []string{
		"\x1b[2mtype:\x1b[0m                  bitbucket  \x1b[2m[default]\x1b[0m\n",
		"/a/b  \x1b[35m[flag]\x1b[0m\n",
		"https://x.example.com  \x1b[32m[file]\x1b[0m\n",
		"b@example.com  \x1b[33m[env]\x1b[0m\n",
		"(not set)  \x1b[2m[" + FromNone + "]\x1b[0m\n",
		"\x1b[2mdisallow:\x1b[0m              a, b\n",
	} {
		if !strings.Contains(col, want) {
			t.Errorf("missing %q in %q", want, col)
		}
	}
	if got := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(col, ""); got != plain {
		t.Errorf("stripped colour differs:\n%s\n%s", got, plain)
	}
}

func TestDeleteSourceBranchPrecedence(t *testing.T) {
	cases := []struct {
		name, file string
		env        map[string]string
		want, src  string
	}{
		{"default is true", "", nil, "true", FromDefault},
		{"file false", "delete_source_branch = false\n", nil, "false", FromFile},
		{"file true", "delete_source_branch = true\n", nil, "true", FromFile},
		{"env beats file", "delete_source_branch = true\n", map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "false"}, "false", FromEnv},
		{"env true beats file false", "delete_source_branch = false\n", map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "TRUE"}, "true", FromEnv},
		{"env 0", "", map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "0"}, "false", FromEnv},
		{"env 1", "delete_source_branch = false\n", map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "1"}, "true", FromEnv},
		{"env No", "", map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "No"}, "false", FromEnv},
		{"env yes", "", map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "yes"}, "true", FromEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			if tc.file != "" {
				s.writeRepo(t, tc.file)
			}
			c, err := Load(s.input(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if c.DeleteSourceBranch != (Value{tc.want, tc.src}) {
				t.Errorf("got %+v, want %s from %s", c.DeleteSourceBranch, tc.want, tc.src)
			}
			if len(c.Warnings) != 0 {
				t.Errorf("a known key must not warn: %v", c.Warnings)
			}
			if got := c.ChildEnv(nil); got[len(got)-1] != "BBAPPROVE_DELETE_SOURCE_BRANCH="+tc.want {
				t.Errorf("child env = %v", got)
			}
			if !strings.Contains(c.Settings(), "delete_source_branch:  "+tc.want+"  ["+tc.src+"]") {
				t.Errorf("settings lack the row:\n%s", c.Settings())
			}
		})
	}
}

func TestDeleteSourceBranchInvalid(t *testing.T) {
	s := newSandbox(t)
	_, err := Load(s.input(map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "maybe"}))
	if err == nil || !strings.Contains(err.Error(), "BBAPPROVE_DELETE_SOURCE_BRANCH") || !strings.Contains(err.Error(), `"maybe"`) {
		t.Errorf("want a clear error, got %v", err)
	}
	s.writeRepo(t, `delete_source_branch = "maybe"`+"\n")
	if _, err := Load(s.input(nil)); err == nil {
		t.Error("a non-boolean file value must be an error")
	}
}

func TestSettingsCompactDisallowOnOneLine(t *testing.T) {
	c := &Config{Type: Value{"bitbucket", FromDefault}, Disallow: []string{"a", "b"}}
	if got := c.SettingsCompactThemed(style.Theme{}); !strings.HasSuffix(got, "disallow:              2 entries: a, b\n") {
		t.Errorf("two entries: %q", got)
	}
	c.Disallow = []string{"a"}
	if got := c.SettingsCompactThemed(style.Theme{}); !strings.HasSuffix(got, "disallow:              1 entry: a\n") {
		t.Errorf("one entry: %q", got)
	}
	c.Disallow = nil
	if got := c.SettingsCompactThemed(style.Theme{}); !strings.HasSuffix(got, "disallow:              (none)\n") {
		t.Errorf("no entries: %q", got)
	}
	if c.SettingsThemed(style.Theme{}) == c.SettingsCompactThemed(style.Theme{}) {
		t.Error("--print-config's list must keep its own format")
	}
}

func TestSettingsWithRowsReplacesNamedRows(t *testing.T) {
	c := &Config{Type: Value{"bitbucket", FromDefault}, Disallow: []string{"a"}, DisallowFile: Value{"/d.txt", FromDefault}}
	got := c.SettingsWithRows(style.Theme{}, map[string]string{"disallow": "a  [env]", "disallow_file": "/d.txt  [default, not found]"})
	for _, want := range []string{"disallow_file:         /d.txt  [default, not found]\n", "disallow:              a  [env]\n", "type:                  bitbucket  [default]\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestLoadFindsAppDataConfigOnWindows(t *testing.T) {
	s := newSandbox(t)
	appdata := s.writeXDG(t, `repository = "https://appdata.example.com"`) // s.xdg stands in for %AppData%
	in := s.input(nil)
	in.Env = func(k string) string {
		if k == "APPDATA" {
			return s.xdg
		}
		return ""
	}
	in.GOOS = "windows"
	c, err := Load(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.File != appdata || c.Repository.Value != "https://appdata.example.com" {
		t.Errorf("File = %q, repository = %q; want the %%AppData%% config %q", c.File, c.Repository.Value, appdata)
	}
}

func TestUsesBuiltIn(t *testing.T) {
	for _, tc := range []struct {
		name, typ, adapterEnv string
		want                  bool
	}{
		{"bitbucket without BBAPPROVE_ADAPTER", "bitbucket", "", true},
		{"bitbucket with BBAPPROVE_ADAPTER", "bitbucket", "/opt/my-adapter", false},
		{"the bash demo type", "demo", "", false},
		{"another forge type", "gitlab", "", false},
		{"another type with BBAPPROVE_ADAPTER", "demo", "/opt/my-adapter", false},
	} {
		if got := UsesBuiltIn(tc.typ, tc.adapterEnv); got != tc.want {
			t.Errorf("%s: UsesBuiltIn(%q, %q) = %v, want %v", tc.name, tc.typ, tc.adapterEnv, got, tc.want)
		}
	}
}

func TestLoadSelectsTheBuiltInAdapter(t *testing.T) {
	s := newSandbox(t)
	for _, tc := range []struct {
		name   string
		file   string
		env    map[string]string
		want   bool
		wantIn string // part of the plain settings list
	}{
		{"default type", "", nil, true, "adapter:               built-in (bitbucket)  [default]"},
		{"type bitbucket in the file", `type = "bitbucket"`, nil, true, "adapter:               built-in (bitbucket)  [file]"},
		{"BBAPPROVE_ADAPTER", "", map[string]string{"BBAPPROVE_ADAPTER": "/opt/ad"}, false, "adapter:               /opt/ad  [env]"},
		{"type demo", `type = "demo"`, nil, false, filepath.Join("adapters", "demo") + "  [file]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := s.input(tc.env)
			in.ConfigFlag = s.writeXDG(t, tc.file+"\n")
			c, err := Load(in)
			if err != nil {
				t.Fatal(err)
			}
			if c.BuiltIn != tc.want {
				t.Errorf("BuiltIn = %v, want %v", c.BuiltIn, tc.want)
			}
			if got := c.Settings(); !strings.Contains(got, tc.wantIn) {
				t.Errorf("settings lack %q:\n%s", tc.wantIn, got)
			}
		})
	}
}
