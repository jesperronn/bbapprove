package bbfake_test

// Characterization tests: run the REAL adapters/bitbucket bash script against the fake Bitbucket Server and pin
// its observable behaviour (exit code, stdout, stderr, and the requests it sends) in golden files. The same
// cases run a second time against the native Go adapter (cmd/bbapprove-bitbucket, built once in TestMain): it
// must produce the bash golden exactly, except where a case carries goDiff. Such a case has its own
// <name>.go.golden.json beside the bash one, so every deliberate difference is explicit and reviewable
// (docs/architecture.md lists them by number). Cases with goOnly exercise Go-only behaviour.
// Refresh goldens with: go test ./internal/bbfake -run Char -update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"bbapprove/internal/adapter"
	"bbapprove/internal/bbfake"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/")

const (
	canary = "CANARY-TOKEN-do-not-print-0123456789"
	opTok  = "op-token-field-value"
	opCred = "op-credential-field-value"
	url5   = "{BASE}/projects/PROJ/repos/repo/pull-requests/5"
)

// toolDir builds a PATH directory holding only what the adapter needs, so a real `op` on the host is never reachable.
func toolDir(t *testing.T) (bash, dir string) {
	t.Helper()
	dir = t.TempDir()
	for _, name := range []string{"bash", "curl", "jq", "tr", "tail", "sed", "cat", "sleep"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("characterization tests need bash, curl and jq (plus tr, tail, sed, cat, sleep) on PATH: %s not found", name)
		}
		if err := os.Symlink(p, filepath.Join(dir, name)); err != nil {
			t.Skipf("cannot symlink tools: %v", err)
		}
		if name == "bash" {
			bash = p
		}
	}
	return bash, dir
}

// listArgs is the one command line `pr list` accepts.
var listArgs = []string{"pr", "list", "--reviewer", "@me", "--state", "open"}

type charCase struct {
	name       string
	args       []string                     // {BASE} and {TMP} are substituted
	sc         func(sc *bbfake.Scenario)    // tweak the standard scenario
	setup      func(s *bbfake.Server)       // inject faults
	env        map[string]string            // extra env; {BASE} and {TMP} substituted
	unset      []string                     // default env vars to drop
	files      map[string]string            // files created under {TMP}
	op         bool                         // put a stub `op` on PATH
	maxTime    string                       // run a copy of the script with CURL_MAX_TIME replaced
	closed     bool                         // shut the server down before running (connection refused)
	maskStderr bool                         // bash only: stderr is tool-version specific (jq), pin only the exit code
	stdin      string                       // fed to the adapter's stdin
	goDiff     string                       // the Go adapter deliberately differs: decision numbers, and a .go.golden.json exists
	goOnly     bool                         // only the Go adapter runs this case (it has no bash behaviour to pin)
	check      func(t *testing.T, r result) // extra semantic assertions
}

type result struct {
	Args     []string         `json:"args"`
	Exit     int              `json:"exit"`
	Stdout   string           `json:"stdout"`
	JSON     any              `json:"stdoutJSON,omitempty"`
	Stderr   string           `json:"stderr"`
	Requests []bbfake.Request `json:"requests"`
	OpCalls  []string         `json:"opCalls,omitempty"`
	raw      string           `json:"-"` // unredacted stdout
	base     string           `json:"-"`
}

func stdScenario() *bbfake.Scenario {
	return &bbfake.Scenario{
		Token: canary, Me: "me",
		PRs: []*bbfake.PR{{
			Project: "PROJ", Repo: "repo", ID: 5, Version: 3, Title: "chore(deps): update x to v1.0.1",
			AuthorEmail: "renovate-bot@example.com", AuthorSlug: "renovate-bot", Branch: "renovate/x", Commit: "abc",
			Reviewers: []bbfake.Person{{Slug: "me", Status: "APPROVED"}, {Slug: "bob", Status: "NEEDS_WORK"}},
			QGStatus:  "PASSED", CommentCount: 4, OpenTasks: 1,
			CommentedActivities: 3, OtherActivities: 1, Tasks: []string{"OPEN", "RESOLVED"},
		}},
		Builds: map[string]bbfake.BuildStats{"abc": {Successful: 2}},
	}
}

// deletes returns the DELETE requests the adapter sent.
func deletes(r result) []bbfake.Request {
	var out []bbfake.Request
	for _, q := range r.Requests {
		if q.Method == "DELETE" {
			out = append(out, q)
		}
	}
	return out
}

func wantDeletes(n int) func(t *testing.T, r result) {
	return func(t *testing.T, r result) {
		t.Helper()
		eq(t, "exit", r.Exit, 0)
		eq(t, "stderr", r.Stderr, "")
		eq(t, "DELETE count", len(deletes(r)), n)
	}
}

func pr5(sc *bbfake.Scenario) *bbfake.PR { return sc.PRs[0] }

func listScenario(sc *bbfake.Scenario) {
	sc.PageSize = 2
	sc.PRs = []*bbfake.PR{
		{Project: "PROJ", Repo: "repo-a", ID: 1, Version: 1, Title: `fix "quoted" & ünïcode`, AuthorEmail: "renovate-bot@example.com", AuthorSlug: "renovate-bot",
			Branch: "renovate/a", Commit: "c1", QGStatus: "FAILED", CommentCount: 2, OpenTasks: 0,
			Reviewers:    []bbfake.Person{{Slug: "alice", Status: "APPROVED"}},
			Participants: []bbfake.Person{{Slug: "me", Status: "APPROVED"}}},
		{Project: "PROJ", Repo: "repo-b", ID: 2, Version: 4, Title: "b", AuthorEmail: "renovate-bot@example.com", AuthorSlug: "renovate-bot",
			Branch: "renovate/b", Commit: "c2", QGStatus: "PASSED", CommentCount: 0, OpenTasks: 3,
			Reviewers: []bbfake.Person{{Slug: "me", Status: "UNAPPROVED"}, {Slug: "bob", Status: "APPROVED"}, {Slug: "carol", Status: "APPROVED"}, {Slug: "dave", Status: "NEEDS_WORK"}, {Slug: "erin", Status: "NEEDS_WORK"}}},
		{Project: "OTH", Repo: "repo-c", ID: 3, Version: 1, Title: "c", AuthorSlug: "someone", Branch: "feature/c", Commit: "c3"},
		{Project: "OTH", Repo: "repo-d", ID: 4, Version: 1, Title: "d", AuthorSlug: "someone", Branch: "feature/d", Commit: "c4", QGStatus: "INPROGRESS"},
		{Project: "OTH", Repo: "repo-e", ID: 6, Version: 1, Title: "no commit", AuthorSlug: "someone", Branch: "feature/e",
			Reviewers: []bbfake.Person{{Slug: "me", Status: "APPROVED"}}},
	}
	sc.Builds = map[string]bbfake.BuildStats{
		"c1": {Successful: 3, Failed: 1, InProgress: 1},
		"c2": {InProgress: 1, Successful: 5},
		"c4": {},
	}
}

var charCases = []charCase{
	// ---- pr list ----
	{name: "list_pages_and_states", args: []string{"pr", "list", "--reviewer", "@me", "--state", "open"}, sc: listScenario, check: func(t *testing.T, r result) {
		var prs []adapter.PR
		mustJSON(t, r.raw, &prs)
		builds, gates := "", ""
		for _, p := range prs {
			builds += p.Build + ","
			gates += p.QualityGate + ","
		}
		eq(t, "builds", builds, "failed,running,none,none,none,")
		eq(t, "gates", gates, "failed,passed,unknown,unknown,unknown,")
		if !prs[0].ApprovedByMe || prs[1].ApprovedByMe || !prs[4].ApprovedByMe {
			t.Errorf("approvedByMe (participants and reviewers count, UNAPPROVED does not): %+v", prs)
		}
		eq(t, "needsWork of #2", prs[1].NeedsWork, 2)
		eq(t, "approvals of #2 (reviewers only)", prs[1].Approvals, 2)
		eq(t, "approvals of #1 (participant approval excluded)", prs[0].Approvals, 1)
	}},
	{name: "list_one_pr_semantics", args: []string{"pr", "list", "--reviewer", "@me", "--state", "open"}, check: func(t *testing.T, r result) {
		var prs []adapter.PR
		mustJSON(t, r.raw, &prs)
		want := adapter.PR{URL: "<BASE>/projects/PROJ/repos/repo/pull-requests/5", Number: 5, Project: "PROJ", Repo: "repo",
			Title: "chore(deps): update x to v1.0.1", HeadBranch: "renovate/x", ApprovedByMe: true, Build: "passed", QualityGate: "passed",
			Approvals: 1, NeedsWork: 1, CommentCount: 4, OpenTasks: 1}
		want.Author.Email, want.Author.Login = "renovate-bot@example.com", "renovate-bot"
		if len(prs) != 1 {
			t.Fatalf("want 1 PR, got %d", len(prs))
		}
		prs[0].URL = strings.ReplaceAll(prs[0].URL, r.base, "<BASE>")
		if !reflect.DeepEqual(prs[0], want) {
			t.Errorf("got %+v\nwant %+v", prs[0], want)
		}
	}},
	{name: "list_empty", args: []string{"pr", "list", "--reviewer", "@me", "--state", "open"}, sc: func(sc *bbfake.Scenario) { sc.PRs = nil }},
	{name: "list_ignores_its_arguments", goDiff: "3", args: []string{"pr", "list", "--reviewer", "someone-else", "--state", "merged", "extra"}},
	{name: "list_whoami_404_means_not_approved", args: listArgs, setup: func(s *bbfake.Server) { s.Fail("GET /whoami", bbfake.Fault{Status: 404, Body: "{}"}) }},
	{name: "list_whoami_empty_body", args: listArgs, sc: func(sc *bbfake.Scenario) { sc.Me = "" }},
	{name: "list_whoami_trimmed", args: listArgs, setup: func(s *bbfake.Server) {
		s.Fail("GET /whoami", bbfake.Fault{Status: 200, Body: " me \r\n"})
	}},
	{name: "list_stats_500_means_build_none", args: listArgs, setup: func(s *bbfake.Server) { s.Fail("POST /stats", bbfake.Fault{Status: 500, Body: "boom"}) }},
	{name: "list_dashboard_500", args: listArgs, setup: func(s *bbfake.Server) { s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 500, Body: "{}"}) }},
	{name: "list_dashboard_401_bad_token", args: listArgs, sc: func(sc *bbfake.Scenario) { sc.Token = "a-different-token" }},
	{name: "list_second_page_fails", args: listArgs, sc: listScenario, setup: func(s *bbfake.Server) {
		s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 200, Body: `{"isLastPage":false,"nextPageStart":2,"values":[]}`}, bbfake.Fault{Status: 503, Body: "{}"})
	}},
	{name: "list_unreachable_server", goDiff: "9", args: listArgs, closed: true},
	{name: "list_dropped_connection", goDiff: "9", args: listArgs, setup: func(s *bbfake.Server) { s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Drop: true}) }},
	{name: "list_slow_server_hits_max_time", goDiff: "9", args: listArgs, maxTime: "0.3",
		setup: func(s *bbfake.Server) {
			s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Delay: 10 * time.Second, Status: 200, Body: "{}"})
		}},
	{name: "list_non_json_200_aborts", goDiff: "5", args: listArgs, maskStderr: true,
		setup: func(s *bbfake.Server) {
			s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 200, Body: "<html>login</html>"})
		}},
	{name: "list_context_path", args: listArgs, sc: func(sc *bbfake.Scenario) { sc.ContextPath = "/bitbucket" }},
	{name: "list_base_url_trailing_slash_is_404", goDiff: "7", args: listArgs, env: map[string]string{"BBAPPROVE_BASE_URL": "{BASE}/"}},

	// ---- pr view ----
	{name: "view_basic", args: []string{"pr", "view", url5}, check: func(t *testing.T, r result) {
		var p adapter.PR
		mustJSON(t, r.raw, &p)
		eq(t, "commentCount (3 COMMENTED activities - 2 tasks)", p.CommentCount, 1)
		eq(t, "openTasks", p.OpenTasks, 1)
		eq(t, "build", p.Build, "passed")
		eq(t, "qualityGate (single-PR GET carries no properties)", p.QualityGate, "unknown")
		eq(t, "approvedByMe", p.ApprovedByMe, true)
	}},
	{name: "view_comment_count_clamped_at_zero", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).CommentedActivities = 0 }},
	{name: "view_no_activity_no_tasks", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).CommentedActivities, pr5(sc).OtherActivities, pr5(sc).Tasks = 0, 0, nil
	}},
	{name: "view_activities_and_tasks_errors_count_as_zero", args: []string{"pr", "view", url5}, setup: func(s *bbfake.Server) {
		s.Fail("GET /activities", bbfake.Fault{Status: 502, Body: "<html>bad gateway</html>"})
		s.Fail("GET /blocker-comments", bbfake.Fault{Status: 500, Body: "{}"})
	}},
	{name: "view_stats_failure_means_build_none", goDiff: "9", args: []string{"pr", "view", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /stats", bbfake.Fault{Drop: true}) }},
	{name: "view_without_head_commit_skips_stats", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).Commit = "" }},
	{name: "view_build_failed_beats_running", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) {
		sc.Builds["abc"] = bbfake.BuildStats{Successful: 5, Failed: 1, InProgress: 2}
	}},
	{name: "view_404", args: []string{"pr", "view", "{BASE}/projects/PROJ/repos/repo/pull-requests/99"}},
	{name: "view_url_host_is_ignored_and_normalized_url_uses_base", args: []string{"pr", "view", "https://other.example.com/projects/PROJ/repos/repo/pull-requests/5/overview?x=1"}},
	{name: "view_bad_url", args: []string{"pr", "view", "https://example.com/not-a-pr"}},
	{name: "view_numeric_id_alone_is_not_a_url", args: []string{"pr", "view", "5"}},
	{name: "view_no_url_argument_unbound_variable", goDiff: "2", args: []string{"pr", "view"}},
	{name: "view_extra_arguments_ignored", goDiff: "3", args: []string{"pr", "view", url5, "--json", "x"}},
	{name: "view_non_json_200_aborts", goDiff: "5", args: []string{"pr", "view", url5}, maskStderr: true,
		setup: func(s *bbfake.Server) { s.Fail("GET /pull-requests/5", bbfake.Fault{Status: 200, Body: "not json"}) }},

	// ---- pr review ----
	{name: "review_approve", args: []string{"pr", "review", url5, "--approve"}, check: func(t *testing.T, r result) {
		eq(t, "stdout", r.raw, "")
	}},
	{name: "review_approve_204_is_success", args: []string{"pr", "review", url5, "--approve"}, setup: func(s *bbfake.Server) {
		s.Fail("POST /approve", bbfake.Fault{Status: 204})
	}},
	{name: "review_approve_403", args: []string{"pr", "review", url5, "--approve"}, setup: func(s *bbfake.Server) {
		s.Fail("POST /approve", bbfake.Fault{Status: 403, Body: "{}"})
	}},
	{name: "review_approve_redirect_is_failure", goDiff: "12", args: []string{"pr", "review", url5, "--approve"}, setup: func(s *bbfake.Server) {
		s.Fail("POST /approve", bbfake.Fault{Status: 302, Body: ""})
	}},
	{name: "review_approve_unreachable", goDiff: "9", args: []string{"pr", "review", url5, "--approve"}, closed: true},
	{name: "review_without_flag", args: []string{"pr", "review", url5}},
	{name: "review_other_flag", args: []string{"pr", "review", url5, "--request-changes"}},
	{name: "review_flag_checked_before_url", args: []string{"pr", "review", "bad", "--comment"}},
	{name: "review_bad_url", args: []string{"pr", "review", "bad", "--approve"}},
	{name: "review_no_arguments_unbound_variable", goDiff: "2", args: []string{"pr", "review"}},

	// ---- pr merge ----
	{name: "merge_ok", args: []string{"pr", "merge", url5}, check: func(t *testing.T, r result) { eq(t, "stdout", r.raw, "") }},
	{name: "merge_retries_stale_version_then_succeeds", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 2 }},
	{name: "merge_stale_every_time_gives_up_after_4_attempts", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 99 }},
	{name: "merge_retries_env_2", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_MERGE_RETRIES": "2"}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 99 }},
	{name: "merge_retries_env_1_means_no_retry", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_MERGE_RETRIES": "1"}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 1 }},
	{name: "merge_retries_env_0_still_tries_once", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_MERGE_RETRIES": "0"}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 1 }},
	{name: "merge_veto_409_is_not_retried", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).MergeVetoes = []string{"Not all required builds are successful yet", "Needs 1 more approval"}
	}},
	{name: "merge_409_without_out_of_date_is_not_retried", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("POST /merge", bbfake.Fault{Status: 409, Body: `{"errors":[{"message":"conflict"}]}`})
	}},
	{name: "merge_out_of_date_on_non_409_is_not_retried", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("POST /merge", bbfake.Fault{Status: 500, Body: `{"errors":[{"message":"out-of-date"}]}`})
	}},
	{name: "merge_post_500_non_json", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /merge", bbfake.Fault{Status: 500, Body: "oops"}) }},
	{name: "merge_post_unreachable_after_get", goDiff: "9", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /merge", bbfake.Fault{Drop: true}) }},
	{name: "merge_post_201_is_success", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /merge", bbfake.Fault{Status: 201, Body: "{}"}) }},
	{name: "merge_version_fetch_404", args: []string{"pr", "merge", "{BASE}/projects/PROJ/repos/repo/pull-requests/99"}},
	{name: "merge_version_fetch_unreachable", goDiff: "9", args: []string{"pr", "merge", url5}, closed: true},
	{name: "merge_version_missing_in_response", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("GET /pull-requests/5", bbfake.Fault{Status: 200, Body: `{"id":5}`}) }},
	{name: "merge_bad_url", args: []string{"pr", "merge", "nope"}},
	{name: "merge_no_arguments_unbound_variable", goDiff: "2", args: []string{"pr", "merge"}},
	{name: "merge_slow_server_hits_max_time", goDiff: "9", args: []string{"pr", "merge", url5}, maxTime: "0.3",
		setup: func(s *bbfake.Server) {
			s.Fail("GET /pull-requests/5", bbfake.Fault{Delay: 10 * time.Second, Status: 200, Body: "{}"})
		}},

	// ---- pr merge: deleting the source branch ----
	{name: "merge_deletes_source_branch_by_default", args: []string{"pr", "merge", url5}, check: func(t *testing.T, r result) {
		eq(t, "stdout", r.raw, "")
		eq(t, "stderr", r.Stderr, "")
		d := deletes(r)
		if len(d) != 1 {
			t.Fatalf("want exactly one DELETE, got %d", len(d))
		}
		eq(t, "path", d[0].Path, "/rest/branch-utils/1.0/projects/PROJ/repos/repo/branches")
		eq(t, "body", d[0].Body, `{"name":"refs/heads/renovate/x","dryRun":false}`)
		eq(t, "authorization", d[0].Authorization, "Bearer <CANARY>")
		eq(t, "content type", d[0].ContentType, "application/json")
		eq(t, "the DELETE comes after the merge POST", r.Requests[len(r.Requests)-2].Method, "POST")
	}},
	{name: "merge_deletes_when_env_true", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "true"}, check: wantDeletes(1)},
	{name: "merge_deletes_when_env_yes_any_case", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "YES"}, check: wantDeletes(1)},
	{name: "merge_keeps_branch_when_env_false", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "false"}, check: wantDeletes(0)},
	{name: "merge_keeps_branch_when_env_0", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "0"}, check: wantDeletes(0)},
	{name: "merge_delete_setting_invalid_is_usage_error_before_merging", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_DELETE_SOURCE_BRANCH": "sometimes"}, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 2)
		eq(t, "requests", len(r.Requests), 0)
	}},
	{name: "merge_fork_pr_does_not_delete", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).ForkProject, pr5(sc).ForkRepo = "~FORKER", "repo"
	}, check: wantDeletes(0)},
	{name: "merge_source_branch_in_same_project_other_repo_does_not_delete", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).ForkProject, pr5(sc).ForkRepo = "PROJ", "repo-fork"
	}, check: wantDeletes(0)},
	{name: "merge_source_is_not_a_branch_does_not_delete", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).FromRefID = "refs/tags/v1"
	}, check: wantDeletes(0)},
	{name: "merge_source_equals_target_does_not_delete", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).Branch = "main"
	}, check: wantDeletes(0)},
	{name: "merge_delete_404_already_gone_is_silent", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).BranchGone = true }, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 0)
		eq(t, "stderr", r.Stderr, "")
		eq(t, "stdout", r.raw, "")
		eq(t, "DELETE count", len(deletes(r)), 1)
	}},
	{name: "merge_delete_200_is_success", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("DELETE /branches", bbfake.Fault{Status: 200, Body: "{}"})
	}, check: func(t *testing.T, r result) { eq(t, "stderr", r.Stderr, ""); eq(t, "exit", r.Exit, 0) }},
	{name: "merge_delete_403_warns_and_still_succeeds", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("DELETE /branches", bbfake.Fault{Status: 403, Body: `{"errors":[{"message":"no permission"}]}`})
	}, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 0)
		eq(t, "stdout", r.raw, "")
		eq(t, "stderr", r.Stderr, "merge ok, but could not delete branch renovate/x (HTTP 403)\n")
	}},
	{name: "merge_delete_500_warns_and_still_succeeds", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("DELETE /branches", bbfake.Fault{Status: 500, Body: "oops"})
	}, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 0)
		eq(t, "stderr", r.Stderr, "merge ok, but could not delete branch renovate/x (HTTP 500)\n")
	}},
	{name: "merge_delete_dropped_connection_warns_and_still_succeeds", goDiff: "9", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("DELETE /branches", bbfake.Fault{Drop: true})
	}, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 0)
		if !strings.HasSuffix(r.Stderr, "merge ok, but could not delete branch renovate/x (HTTP 000)\n") {
			t.Errorf("stderr = %q", r.Stderr) // curl's own error line comes first
		}
	}},
	{name: "merge_veto_does_not_delete", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).MergeVetoes = []string{"Needs 1 more approval"}
	}, check: func(t *testing.T, r result) { eq(t, "exit", r.Exit, 1); eq(t, "DELETE count", len(deletes(r)), 0) }},
	{name: "merge_out_of_date_exhausted_does_not_delete", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 99 }, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 1)
		eq(t, "DELETE count", len(deletes(r)), 0)
	}},
	{name: "merge_retry_then_success_deletes_exactly_once", args: []string{"pr", "merge", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleMerges = 2 }, check: func(t *testing.T, r result) {
		eq(t, "exit", r.Exit, 0)
		eq(t, "DELETE count", len(deletes(r)), 1)
		eq(t, "the DELETE is the last request", r.Requests[len(r.Requests)-1].Method, "DELETE")
	}},

	// ---- pr comment ----
	{name: "comment_body", args: []string{"pr", "comment", url5, "--body", "LGTM"}, check: func(t *testing.T, r result) { eq(t, "stdout", r.raw, "") }},
	{name: "comment_body_is_json_escaped", args: []string{"pr", "comment", url5, "--body", "line one\nline \"two\" \\ ünï 🙂\t<b>&"}},
	{name: "comment_body_file_trailing_newlines_stripped", args: []string{"pr", "comment", url5, "--body-file", "{TMP}/body.txt"}, files: map[string]string{"body.txt": "first\n\nlast line\n\n\n"}},
	{name: "comment_body_file_missing", goDiff: "file read error without cat text", args: []string{"pr", "comment", url5, "--body-file", "{TMP}/nope.txt"}},
	{name: "comment_body_file_whitespace_only", args: []string{"pr", "comment", url5, "--body-file", "{TMP}/ws.txt"}, files: map[string]string{"ws.txt": " \n\t\n"}},
	{name: "comment_last_body_wins", args: []string{"pr", "comment", url5, "--body", "one", "--body", "two"}},
	{name: "comment_body_and_body_file", args: []string{"pr", "comment", url5, "--body", "x", "--body-file", "{TMP}/b.txt"}, files: map[string]string{"b.txt": "y"}},
	{name: "comment_body_file_then_body", args: []string{"pr", "comment", url5, "--body-file", "{TMP}/b.txt", "--body", "x"}, files: map[string]string{"b.txt": "y"}},
	{name: "comment_empty_body", args: []string{"pr", "comment", url5, "--body", ""}},
	{name: "comment_blank_body", args: []string{"pr", "comment", url5, "--body", "  \n "}},
	{name: "comment_no_body", args: []string{"pr", "comment", url5}},
	{name: "comment_body_needs_value", args: []string{"pr", "comment", url5, "--body"}},
	{name: "comment_body_file_needs_value", args: []string{"pr", "comment", url5, "--body-file"}},
	{name: "comment_unknown_argument", args: []string{"pr", "comment", url5, "--bogus"}},
	{name: "comment_positional_text_is_unknown_argument", args: []string{"pr", "comment", url5, "hello"}},
	{name: "comment_bad_url", args: []string{"pr", "comment", "bad", "--body", "x"}},
	{name: "comment_no_arguments", goDiff: "2", args: []string{"pr", "comment"}},
	{name: "comment_500", args: []string{"pr", "comment", url5, "--body", "x"}, setup: func(s *bbfake.Server) { s.Fail("POST /comments", bbfake.Fault{Status: 500, Body: "{}"}) }},
	{name: "comment_unreachable", goDiff: "9", args: []string{"pr", "comment", url5, "--body", "x"}, closed: true},

	// ---- pr close ----
	{name: "close_decline_only", args: []string{"pr", "close", url5}, check: func(t *testing.T, r result) { eq(t, "stdout", r.raw, "") }},
	{name: "close_comment_then_decline", args: []string{"pr", "close", url5, "--comment", "superseded by #6"}},
	{name: "close_comment_failure_aborts_decline", args: []string{"pr", "close", url5, "--comment", "x"}, setup: func(s *bbfake.Server) {
		s.Fail("POST /comments", bbfake.Fault{Status: 500, Body: "{}"})
	}},
	{name: "close_decline_409", goDiff: "12b", args: []string{"pr", "close", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 1 }},
	{name: "close_decline_is_not_retried_after_comment", goDiff: "12b", args: []string{"pr", "close", url5, "--comment", "x"}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 1 }},
	{name: "close_decline_500_non_json", args: []string{"pr", "close", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /decline", bbfake.Fault{Status: 500, Body: "oops"}) }},
	{name: "close_version_fetch_404", args: []string{"pr", "close", "{BASE}/projects/PROJ/repos/repo/pull-requests/99"}},
	{name: "close_version_missing_in_response", args: []string{"pr", "close", url5}, setup: func(s *bbfake.Server) { s.Fail("GET /pull-requests/5", bbfake.Fault{Status: 200, Body: "{}"}) }},
	{name: "close_comment_empty", args: []string{"pr", "close", url5, "--comment", ""}},
	{name: "close_comment_blank", args: []string{"pr", "close", url5, "--comment", " \t"}},
	{name: "close_comment_needs_value", args: []string{"pr", "close", url5, "--comment"}},
	{name: "close_unknown_argument", args: []string{"pr", "close", url5, "--force"}},
	{name: "close_bad_url", args: []string{"pr", "close", "bad"}},
	{name: "close_no_arguments", goDiff: "2", args: []string{"pr", "close"}},
	{name: "close_unreachable", goDiff: "9", args: []string{"pr", "close", url5}, closed: true},

	// ---- auth token ----
	{name: "auth_token_env_wins_and_op_is_not_called", args: []string{"auth", "token"}, op: true,
		env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM", "OP_TOKEN": opTok}, check: func(t *testing.T, r result) {
			eq(t, "stdout", r.raw, canary+"\n")
		}},
	{name: "auth_token_from_op_token_field", args: []string{"auth", "token"}, op: true, unset: []string{"BBAPPROVE_TOKEN"},
		env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM", "OP_TOKEN": opTok, "OP_CRED": opCred}, check: func(t *testing.T, r result) {
			eq(t, "stdout", r.raw, opTok+"\n")
		}},
	{name: "auth_token_falls_back_to_credential_field", args: []string{"auth", "token"}, op: true, unset: []string{"BBAPPROVE_TOKEN"},
		env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM", "OP_CRED": opCred}, check: func(t *testing.T, r result) {
			eq(t, "stdout", r.raw, opCred+"\n")
		}},
	{name: "auth_token_op_fails_for_both_fields", args: []string{"auth", "token"}, op: true, unset: []string{"BBAPPROVE_TOKEN"},
		env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM"}},
	{name: "auth_token_waiting_notice_is_dimmed_under_force_color", args: []string{"auth", "token"}, op: true, unset: []string{"BBAPPROVE_TOKEN"},
		env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM", "OP_TOKEN": opTok, "FORCE_COLOR": "1"}},
	{name: "auth_token_no_color_beats_force_color", args: []string{"auth", "token"}, op: true, unset: []string{"BBAPPROVE_TOKEN"},
		env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM", "OP_TOKEN": opTok, "FORCE_COLOR": "1", "NO_COLOR": "1"}},
	{name: "auth_token_op_not_installed", args: []string{"auth", "token"}, unset: []string{"BBAPPROVE_TOKEN"}, env: map[string]string{"BBAPPROVE_TOKEN_NAME": "ITEM"}},
	{name: "auth_token_no_token_and_no_name", args: []string{"auth", "token"}, op: true, unset: []string{"BBAPPROVE_TOKEN"}},
	{name: "auth_token_needs_base_url", goDiff: "8", args: []string{"auth", "token"}, unset: []string{"BBAPPROVE_BASE_URL"}},
	{name: "auth_token_makes_no_requests", goDiff: "3", args: []string{"auth", "token", "extra", "args"}},

	// ---- environment and usage errors ----
	{name: "no_base_url", args: []string{"pr", "list"}, unset: []string{"BBAPPROVE_BASE_URL"}},
	{name: "no_base_url_no_arguments", args: nil, unset: []string{"BBAPPROVE_BASE_URL"}},
	{name: "empty_base_url", args: []string{"pr", "list"}, env: map[string]string{"BBAPPROVE_BASE_URL": ""}},
	{name: "no_token_pr_list", args: []string{"pr", "list"}, unset: []string{"BBAPPROVE_TOKEN"}},
	{name: "no_token_pr_view", args: []string{"pr", "view", url5}, unset: []string{"BBAPPROVE_TOKEN"}},
	{name: "empty_token", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_TOKEN": ""}},
	{name: "no_token_beats_usage_error", args: []string{"bogus"}, unset: []string{"BBAPPROVE_TOKEN"}},
	{name: "usage_no_arguments", args: nil},
	{name: "usage_pr_without_subcommand", args: []string{"pr"}},
	{name: "usage_unknown_pr_subcommand", args: []string{"pr", "reopen", url5}},
	{name: "usage_unknown_group", args: []string{"issue", "list"}},
	{name: "usage_auth_without_token", args: []string{"auth"}},
	{name: "usage_help_flag_is_not_special", args: []string{"--help"}},

	// ---- both implementations: input the Go port must escape and read exactly like jq and cat ----
	{name: "comment_body_control_characters_are_escaped", args: []string{"pr", "comment", url5, "--body", "a\x01b\x7fc\x08d\x0ce\x1f <&> \u2028"}},
	{name: "comment_body_file_from_stdin", args: []string{"pr", "comment", url5, "--body-file", "-"}, stdin: "from stdin\nsecond line\n\n"},
	{name: "comment_body_file_from_stdin_blank", args: []string{"pr", "comment", url5, "--body-file", "-"}, stdin: " \n"},
	{name: "view_title_and_branch_escapes", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) {
		pr5(sc).Title = "t\x01\x7f\u2028 <&> \"q\" \\ \u00e9 \U0001F642"
		pr5(sc).Branch = "feature/\x1f"
	}},

	// ---- Go adapter only: the deliberate fixes (numbers refer to docs/architecture.md) ----
	{name: "list_needs_both_flags", goOnly: true, goDiff: "3", args: []string{"pr", "list"}},
	{name: "list_wrong_reviewer", goOnly: true, goDiff: "3", args: []string{"pr", "list", "--reviewer", "bob", "--state", "open"}},
	{name: "list_wrong_state", goOnly: true, goDiff: "3", args: []string{"pr", "list", "--reviewer", "@me", "--state", "merged"}},
	{name: "list_unknown_argument", goOnly: true, goDiff: "3", args: []string{"pr", "list", "--reviewer", "@me", "--state", "open", "--all"}},
	{name: "list_flags_in_either_order", goOnly: true, goDiff: "3", args: []string{"pr", "list", "--state", "open", "--reviewer", "@me"}},
	{name: "list_more_pages_without_next_page_start", goOnly: true, goDiff: "4", args: listArgs, setup: func(s *bbfake.Server) {
		s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 200, Body: `{"isLastPage":false,"values":[]}`})
	}},
	{name: "list_next_page_start_does_not_advance", goOnly: true, goDiff: "4", args: listArgs, setup: func(s *bbfake.Server) {
		s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 200, Body: `{"isLastPage":false,"nextPageStart":0,"values":[]}`})
	}},
	{name: "list_base_url_trailing_slashes_trimmed", goOnly: true, goDiff: "7", args: listArgs, env: map[string]string{"BBAPPROVE_BASE_URL": "{BASE}///"}, check: func(t *testing.T, r result) {
		var prs []adapter.PR
		mustJSON(t, r.raw, &prs)
		eq(t, "url", strings.ReplaceAll(prs[0].URL, r.base, "<BASE>"), "<BASE>/projects/PROJ/repos/repo/pull-requests/5")
	}},
	{name: "list_redirect_names_location", goOnly: true, goDiff: "12", args: listArgs, setup: func(s *bbfake.Server) {
		s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 301, Headers: map[string]string{"Location": "https://login.example.com/sso"}})
	}},
	{name: "merge_redirect_names_location", goOnly: true, goDiff: "12", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) {
		s.Fail("POST /merge", bbfake.Fault{Status: 302, Headers: map[string]string{"Location": "https://login.example.com/sso"}})
	}},
	{name: "view_activities_are_paged", goOnly: true, goDiff: "6", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) {
		sc.ItemPageSize = 2
		pr5(sc).CommentedActivities, pr5(sc).OtherActivities, pr5(sc).Tasks = 5, 2, []string{"OPEN", "RESOLVED"}
	}, check: func(t *testing.T, r result) {
		var p adapter.PR
		mustJSON(t, r.raw, &p)
		eq(t, "commentCount (5 COMMENTED - 2 tasks)", p.CommentCount, 3)
		eq(t, "openTasks", p.OpenTasks, 1)
	}},
	{name: "view_blocker_comments_are_paged", goOnly: true, goDiff: "6", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) {
		sc.ItemPageSize = 2
		pr5(sc).CommentedActivities, pr5(sc).OtherActivities, pr5(sc).Tasks = 6, 0, []string{"RESOLVED", "OPEN", "RESOLVED", "OPEN", "OPEN"}
	}, check: func(t *testing.T, r result) {
		var p adapter.PR
		mustJSON(t, r.raw, &p)
		eq(t, "commentCount (6 COMMENTED - 5 tasks)", p.CommentCount, 1)
		eq(t, "openTasks (on the third page too)", p.OpenTasks, 3)
	}},
	{name: "view_stats_200_not_json", goOnly: true, goDiff: "5", args: []string{"pr", "view", url5}, setup: func(s *bbfake.Server) {
		s.Fail("POST /stats", bbfake.Fault{Status: 200, Body: "<html>"})
	}},
	{name: "view_extra_arguments_rejected_after_url", goOnly: true, goDiff: "3", args: []string{"pr", "view", url5, "extra"}},
	{name: "review_extra_argument", goOnly: true, goDiff: "3", args: []string{"pr", "review", url5, "--approve", "extra"}},
	{name: "merge_extra_argument", goOnly: true, goDiff: "3", args: []string{"pr", "merge", url5, "extra"}},
	{name: "merge_retries_not_a_number", goOnly: true, goDiff: "11", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_MERGE_RETRIES": "many"}},
	{name: "merge_retries_negative", goOnly: true, goDiff: "11", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_MERGE_RETRIES": "-1"}},
	{name: "merge_retry_sleep_not_an_integer", goOnly: true, goDiff: "11", args: []string{"pr", "merge", url5}, env: map[string]string{"BBAPPROVE_RETRY_SLEEP": "1.5"}},
	{name: "close_retry_sleep_invalid", goOnly: true, goDiff: "11", args: []string{"pr", "close", url5}, env: map[string]string{"BBAPPROVE_RETRY_SLEEP": "-2"}},
	{name: "close_decline_retries_stale_version_then_succeeds", goOnly: true, goDiff: "12b", args: []string{"pr", "close", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 2 }},
	{name: "close_decline_stale_every_time_gives_up_after_4_attempts", goOnly: true, goDiff: "12b", args: []string{"pr", "close", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 99 }},
	{name: "close_decline_retries_env_2", goOnly: true, goDiff: "12b", args: []string{"pr", "close", url5}, env: map[string]string{"BBAPPROVE_MERGE_RETRIES": "2"}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 99 }},
}

// impl is one adapter implementation under test.
type impl struct {
	name   string
	cmd    string   // executable
	prefix []string // arguments before the adapter's own (the script for bash)
	path   string   // PATH for the adapter: only what it may use
	script string   // path shown as <ADAPTER> in redactions
	src    string   // bash source, to build the short-timeout copy
	bash   string   // shell for the op stub's shebang
}

func TestAdapterCharacterization(t *testing.T) {
	t.Run("bash", func(t *testing.T) {
		bash, tools := toolDir(t)
		script, err := filepath.Abs("../../adapters/bitbucket")
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		runAll(t, impl{name: "bash", cmd: bash, prefix: []string{script}, path: tools, script: script, src: string(src), bash: bash})
	})
	t.Run("go", func(t *testing.T) {
		shell := "/bin/sh"
		if p, err := exec.LookPath("bash"); err == nil {
			shell = p
		}
		runAll(t, impl{name: "go", cmd: goBinary, path: t.TempDir(), script: goBinary, bash: shell})
	})
}

func runAll(t *testing.T, im impl) {
	for _, tc := range charCases {
		if tc.goOnly && im.name != "go" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc, im)
		})
	}
}

func runCase(t *testing.T, tc charCase, im impl) {
	tmp := t.TempDir()
	sc := stdScenario()
	if tc.sc != nil {
		tc.sc(sc)
	}
	srv := bbfake.New(sc)
	t.Cleanup(srv.Close)
	if tc.setup != nil {
		tc.setup(srv)
	}
	base := srv.URL()
	if tc.closed {
		srv.Close()
	}
	subst := strings.NewReplacer("{BASE}", base, "{TMP}", tmp)

	for name, content := range tc.files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script, argv := im.script, append([]string(nil), im.prefix...)
	extraEnv := map[string]string{}
	if tc.maxTime != "" {
		if im.name == "bash" {
			const orig = "CURL_MAX_TIME=3.5 "
			if !strings.Contains(im.src, orig) {
				t.Fatal("adapter no longer declares CURL_MAX_TIME=3.5; update the characterization test")
			}
			script = filepath.Join(tmp, "bitbucket-short-timeout")
			if err := os.WriteFile(script, []byte(strings.Replace(im.src, orig, "CURL_MAX_TIME="+tc.maxTime+" ", 1)), 0o700); err != nil {
				t.Fatal(err)
			}
			argv = []string{script}
		} else {
			extraEnv["BBAPPROVE_BITBUCKET_TEST_MAX_TIME"] = tc.maxTime + "s"
		}
	}
	path := im.path
	opLog := filepath.Join(tmp, "op.log")
	if tc.op {
		opDir := filepath.Join(tmp, "opbin")
		if err := os.Mkdir(opDir, 0o700); err != nil {
			t.Fatal(err)
		}
		stub := "#!" + im.bash + "\n" + `echo "$*" >>"$OP_LOG"
case "$*" in
  *label=token) v="${OP_TOKEN:-}" ;;
  *label=credential) v="${OP_CRED:-}" ;;
esac
[ -n "$v" ] || { echo "op: field not found" >&2; exit 1; }
echo "$v"
`
		if err := os.WriteFile(filepath.Join(opDir, "op"), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
		path = opDir + string(os.PathListSeparator) + path
	}

	env := map[string]string{
		"PATH": path, "HOME": tmp, "LC_ALL": "C", "NO_PROXY": "*", "OP_LOG": opLog,
		"BBAPPROVE_BASE_URL": base, "BBAPPROVE_TOKEN": canary, "BBAPPROVE_RETRY_SLEEP": "0",
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	for k, v := range tc.env {
		env[k] = subst.Replace(v)
	}
	for _, k := range tc.unset {
		delete(env, k)
	}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	var args []string
	for _, a := range tc.args {
		args = append(args, subst.Replace(a))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, im.cmd, append(argv, args...)...)
	cmd.Dir, cmd.Env = tmp, environ
	cmd.Stdin = strings.NewReader(tc.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exit := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running adapter: %v", err)
		}
		exit = ee.ExitCode()
	}

	red := newRedactor(base, tmp, script)
	r := result{Exit: exit, Stdout: red.text(stdout.String()), Stderr: red.text(stderr.String()), raw: stdout.String(), base: base}
	for _, a := range tc.args {
		r.Args = append(r.Args, a)
	}
	if tc.maskStderr && im.name == "bash" {
		// jq's wording and exit code for a parse error differ between versions: pin only "failed".
		r.Stderr = "<tool-specific stderr not pinned>"
		if r.Exit == 0 {
			t.Errorf("expected a non-zero exit")
		}
		r.Exit = -1
	}
	if tc.maxTime != "" {
		r.Args = append([]string{"(CURL_MAX_TIME=" + tc.maxTime + ")"}, r.Args...)
	}
	var js any
	if json.Unmarshal([]byte(r.Stdout), &js) == nil {
		r.JSON = js
	}
	for _, q := range srv.Requests() {
		q.Authorization = red.text(q.Authorization)
		q.Path = red.text(q.Path)
		q.Body = red.text(q.Body)
		r.Requests = append(r.Requests, q)
	}
	if b, err := os.ReadFile(opLog); err == nil {
		r.OpCalls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	// `auth token` prints the token on purpose (BBAPPROVE_TOKEN wins); nothing else may ever show it.
	leaks := []string{stderr.String()}
	if len(tc.args) < 2 || tc.args[0] != "auth" {
		leaks = append(leaks, stdout.String())
	}
	for _, s := range leaks {
		if strings.Contains(s, canary) {
			t.Errorf("the token canary leaked into adapter output")
		}
	}
	if tc.check != nil {
		tc.check(t, r)
	}
	checkGolden(t, tc, im.name, r)
}

type redactor struct{ rep *strings.Replacer }

var (
	reLine = regexp.MustCompile(`line [0-9]+:`)
	reCurl = regexp.MustCompile(`(?m)^curl: .*$`)
)

func newRedactor(base, tmp, script string) redactor {
	return redactor{strings.NewReplacer(
		base, "<BASE>", tmp, "<TMP>", script, "<ADAPTER>", canary, "<CANARY>", opTok, "<TOKEN>", opCred, "<TOKEN>",
	)}
}

func (r redactor) text(s string) string {
	s = r.rep.Replace(s)
	s = reLine.ReplaceAllString(s, "line N:")
	return reCurl.ReplaceAllString(s, "curl: <curl error text>")
}

func goldenPath(name string, goSide bool) string {
	suffix := ".golden.json"
	if goSide {
		suffix = ".go.golden.json"
	}
	return filepath.Join("testdata", "adapter", name+suffix)
}

// checkGolden compares r with the right golden. The bash side owns <name>.golden.json. The Go side must equal it
// unless the case declares goDiff (or goOnly), in which case <name>.go.golden.json is its own record.
func checkGolden(t *testing.T, tc charCase, impl string, r result) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	bashPath := goldenPath(tc.name, false)
	switch {
	case impl == "bash":
		compareGolden(t, tc.name, bashPath, got, true)
	case tc.goOnly:
		compareGolden(t, tc.name, goldenPath(tc.name, true), got, true)
	case tc.goDiff != "":
		if want, err := os.ReadFile(bashPath); err == nil && bytes.Equal(want, got) {
			t.Errorf("goDiff %q is set but the Go output equals the bash golden: drop goDiff and %s", tc.goDiff, goldenPath(tc.name, true))
		}
		compareGolden(t, tc.name, goldenPath(tc.name, true), got, true)
	default:
		// The Go adapter must match the bash golden; never rewrite it from the Go side.
		compareGolden(t, tc.name, bashPath, got, false)
	}
}

func compareGolden(t *testing.T, name, p string, got []byte, writable bool) {
	t.Helper()
	if *updateGolden && writable {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/bbfake -run Char -update)", err)
	}
	if bytes.Equal(want, got) {
		return
	}
	if !writable {
		t.Errorf("the Go adapter differs from the bash golden %s: fix it, or declare the difference with goDiff (decision numbers) and record %s\n--- got ---\n%s\n--- want ---\n%s",
			p, goldenPath(name, true), got, want)
		return
	}
	// Distinguish a semantic change from formatting only (key order, whitespace) in stdout.
	var w, g result
	if json.Unmarshal(want, &w) == nil && json.Unmarshal(got, &g) == nil && w.Exit == g.Exit && w.Stderr == g.Stderr && reflect.DeepEqual(w.JSON, g.JSON) && w.Stdout != g.Stdout {
		t.Errorf("stdout is semantically equal JSON but its text differs (key order or whitespace)\n got: %s\nwant: %s", g.Stdout, w.Stdout)
		return
	}
	t.Errorf("%s differs from the golden file (run: go test ./internal/bbfake -run Char -update)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
}

// TestGoGoldensHaveCases keeps the Go-only goldens honest: each belongs to a case that declares goDiff or goOnly.
func TestGoGoldensHaveCases(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "adapter", "*.go.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, tc := range charCases {
		if tc.goOnly || tc.goDiff != "" {
			want[goldenPath(tc.name, true)] = true
		}
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("%s belongs to no case with goDiff or goOnly", f)
		}
		delete(want, f)
	}
	for f := range want {
		t.Errorf("%s is missing", f)
	}
}

// TestAdapterTimeoutConstants pins the curl timeouts the characterization cases stand in for: the slow-server
// cases run a copy with a shorter --max-time so the suite stays fast.
func TestAdapterTimeoutConstants(t *testing.T) {
	b, err := os.ReadFile("../../adapters/bitbucket")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CURL_CONNECT_TIMEOUT=2.7 ", "CURL_MAX_TIME=3.5 ", `MERGE_RETRIES="${BBAPPROVE_MERGE_RETRIES:-4}"`, `RETRY_SLEEP="${BBAPPROVE_RETRY_SLEEP:-2}"`, `--connect-timeout "$CURL_CONNECT_TIMEOUT" --max-time "$CURL_MAX_TIME"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("adapter no longer contains %q", want)
		}
	}
}

func mustJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("stdout is not the expected JSON: %v\n%s", err, s)
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
