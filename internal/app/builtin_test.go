package app

import (
	"strings"
	"testing"

	"bbapprove/internal/bbfake"
)

// builtinEnv is an environment with no BBAPPROVE_ADAPTER: the type defaults to bitbucket, so the core calls
// the adapter built into the binary and never executes anything.
func builtinEnv(t *testing.T, srv *bbfake.Server, token string) []string {
	t.Helper()
	return []string{
		"BBAPPROVE_REPOSITORY=" + srv.URL(), "BBAPPROVE_AUTHOR_EMAIL=renovate-bot@example.com",
		"BBAPPROVE_TOKEN=" + token, "XDG_CONFIG_HOME=" + t.TempDir(), "NO_COLOR=1", "NO_PROXY=*",
	}
}

func builtinScenario() *bbfake.Scenario {
	return &bbfake.Scenario{
		Token: "secret-token", Me: "me",
		PRs: []*bbfake.PR{{
			Project: "PROJ", Repo: "repo", ID: 5, Version: 3, Title: "chore(deps): update x to v1.0.1",
			AuthorEmail: "renovate-bot@example.com", AuthorSlug: "renovate-bot", Branch: "renovate/x", Commit: "abc",
			Reviewers: []bbfake.Person{{Slug: "me", Status: "UNAPPROVED"}},
			QGStatus:  "PASSED",
		}},
		Builds: map[string]bbfake.BuildStats{"abc": {Successful: 2}},
	}
}

// A list through the core with the built-in adapter reaches the (stand-in) Bitbucket server in process, on every OS.
func TestBuiltInAdapterListsInProcess(t *testing.T) {
	srv := bbfake.New(builtinScenario())
	t.Cleanup(srv.Close)
	code, out, errOut := run(t, builtinEnv(t, srv, "secret-token"))
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q, stdout:\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "PROJ/repo#5") || !strings.Contains(out, "update x to v1.0.1") || !strings.Contains(out, "Dry run") {
		t.Errorf("the PR is missing from the table:\n%s", out)
	}
	if len(srv.Requests()) == 0 {
		t.Error("the stand-in server saw no request")
	}
	for _, q := range srv.Requests() {
		if q.Authorization != "Bearer secret-token" {
			t.Errorf("%s %s sent Authorization %q", q.Method, q.Path, q.Authorization)
		}
	}
}

// A usage-level adapter failure keeps its exit code semantics: the core prints the adapter's one-line error and exits 1.
func TestBuiltInAdapterFailureIsReported(t *testing.T) {
	srv := bbfake.New(builtinScenario())
	t.Cleanup(srv.Close)
	code, out, errOut := run(t, builtinEnv(t, srv, "wrong-token"))
	if code != 1 || out != "" || errOut == "" || strings.Contains(errOut, "wrong-token") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestBuiltInAdapterApproveAndMerge(t *testing.T) {
	sc := builtinScenario()
	srv := bbfake.New(sc)
	t.Cleanup(srv.Close)
	url := srv.URL() + "/projects/PROJ/repos/repo/pull-requests/5"
	code, out, errOut := run(t, builtinEnv(t, srv, "secret-token"), "--apply", url)
	if code != 0 {
		t.Fatalf("code %d, stderr %q, stdout:\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "MERGED") {
		t.Errorf("not merged:\n%s", out)
	}
	var methods []string
	for _, q := range srv.Requests() {
		methods = append(methods, q.Method+" "+q.Path)
	}
	joined := strings.Join(methods, "\n")
	for _, want := range []string{"/approve", "/merge"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no request to %s:\n%s", want, joined)
		}
	}
}
