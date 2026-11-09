package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		repo   Value
		author Value
	}{
		{"defaults only", "", nil, val("", FromNone), val("", FromNone)},
		{"file", fileA, nil, val("https://file.example.com", FromFile), val("file-bot@example.com", FromFile)},
		{"env beats file", fileA, map[string]string{
			"BBAPPROVE_REPOSITORY": "https://env.example.com", "BBAPPROVE_AUTHOR_EMAIL": "env-bot@example.com",
		}, val("https://env.example.com", FromEnv), val("env-bot@example.com", FromEnv)},
		{"env only", "", map[string]string{"BBAPPROVE_REPOSITORY": "https://env.example.com"},
			val("https://env.example.com", FromEnv), val("", FromNone)},
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
			if c.Repository != tc.repo || c.AuthorEmail != tc.author {
				t.Errorf("repo %+v author %+v", c.Repository, c.AuthorEmail)
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
	if got := env[len(env)-1]; got != "BBAPPROVE_BASE_URL=https://file.example.com" {
		t.Errorf("%v", env)
	}
	d := c.Describe()
	for _, w := range []string{"repository:      https://file.example.com  [file]", "[default]"} {
		if !strings.Contains(d, w) {
			t.Errorf("describe lacks %q:\n%s", w, d)
		}
	}
	if strings.Contains(d, "user_token_name") || strings.Contains(d, "TOKEN") {
		t.Errorf("describe still mentions the token item:\n%s", d)
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
	if d := c.Describe(); !strings.Contains(d, "update_repo:     env/repo  [env]") {
		t.Errorf("Describe lacks update_repo:\n%s", d)
	}
}

func TestLeftoverUserTokenNameWarnsOnce(t *testing.T) {
	s := newSandbox(t)
	p := s.writeRepo(t, "repository = \"https://h.example.com\"\nuser_token_name = \"X\"\nbogus = 1\n")
	c, err := Load(s.input(nil))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, w := range c.Warnings {
		if strings.Contains(w, "user_token_name") {
			n++
			if w != "user_token_name in "+p+" is no longer used; set BBAPPROVE_TOKEN instead (see README)" {
				t.Errorf("warning text: %q", w)
			}
		}
	}
	if n != 1 || len(c.Warnings) != 2 {
		t.Errorf("warnings: %q", c.Warnings)
	}
}
