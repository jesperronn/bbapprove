package bbfake_test

// Characterization tests: run the REAL adapters/bitbucket bash script against the fake Bitbucket Server and pin
// its observable behaviour (exit code, stdout, stderr, and the requests it sends) in golden files. They exist
// so a later native Go port of the adapter can be proven identical by pointing the same cases at it.
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
	maskStderr bool                         // stderr is tool-version specific (jq): pin only the exit code
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
	{name: "list_ignores_its_arguments", args: []string{"pr", "list", "--reviewer", "someone-else", "--state", "merged", "extra"}},
	{name: "list_whoami_404_means_not_approved", args: []string{"pr", "list"}, setup: func(s *bbfake.Server) { s.Fail("GET /whoami", bbfake.Fault{Status: 404, Body: "{}"}) }},
	{name: "list_whoami_empty_body", args: []string{"pr", "list"}, sc: func(sc *bbfake.Scenario) { sc.Me = "" }},
	{name: "list_whoami_trimmed", args: []string{"pr", "list"}, setup: func(s *bbfake.Server) {
		s.Fail("GET /whoami", bbfake.Fault{Status: 200, Body: " me \r\n"})
	}},
	{name: "list_stats_500_means_build_none", args: []string{"pr", "list"}, setup: func(s *bbfake.Server) { s.Fail("POST /stats", bbfake.Fault{Status: 500, Body: "boom"}) }},
	{name: "list_dashboard_500", args: []string{"pr", "list"}, setup: func(s *bbfake.Server) { s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 500, Body: "{}"}) }},
	{name: "list_dashboard_401_bad_token", args: []string{"pr", "list"}, sc: func(sc *bbfake.Scenario) { sc.Token = "a-different-token" }},
	{name: "list_second_page_fails", args: []string{"pr", "list"}, sc: listScenario, setup: func(s *bbfake.Server) {
		s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 200, Body: `{"isLastPage":false,"nextPageStart":2,"values":[]}`}, bbfake.Fault{Status: 503, Body: "{}"})
	}},
	{name: "list_unreachable_server", args: []string{"pr", "list"}, closed: true},
	{name: "list_dropped_connection", args: []string{"pr", "list"}, setup: func(s *bbfake.Server) { s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Drop: true}) }},
	{name: "list_slow_server_hits_max_time", args: []string{"pr", "list"}, maxTime: "0.3",
		setup: func(s *bbfake.Server) {
			s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Delay: 10 * time.Second, Status: 200, Body: "{}"})
		}},
	{name: "list_non_json_200_aborts", args: []string{"pr", "list"}, maskStderr: true,
		setup: func(s *bbfake.Server) {
			s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 200, Body: "<html>login</html>"})
		}},
	{name: "list_context_path", args: []string{"pr", "list"}, sc: func(sc *bbfake.Scenario) { sc.ContextPath = "/bitbucket" }},
	{name: "list_base_url_trailing_slash_is_404", args: []string{"pr", "list"}, env: map[string]string{"BBAPPROVE_BASE_URL": "{BASE}/"}},

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
	{name: "view_stats_failure_means_build_none", args: []string{"pr", "view", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /stats", bbfake.Fault{Drop: true}) }},
	{name: "view_without_head_commit_skips_stats", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).Commit = "" }},
	{name: "view_build_failed_beats_running", args: []string{"pr", "view", url5}, sc: func(sc *bbfake.Scenario) {
		sc.Builds["abc"] = bbfake.BuildStats{Successful: 5, Failed: 1, InProgress: 2}
	}},
	{name: "view_404", args: []string{"pr", "view", "{BASE}/projects/PROJ/repos/repo/pull-requests/99"}},
	{name: "view_url_host_is_ignored_and_normalized_url_uses_base", args: []string{"pr", "view", "https://other.example.com/projects/PROJ/repos/repo/pull-requests/5/overview?x=1"}},
	{name: "view_bad_url", args: []string{"pr", "view", "https://example.com/not-a-pr"}},
	{name: "view_numeric_id_alone_is_not_a_url", args: []string{"pr", "view", "5"}},
	{name: "view_no_url_argument_unbound_variable", args: []string{"pr", "view"}},
	{name: "view_extra_arguments_ignored", args: []string{"pr", "view", url5, "--json", "x"}},
	{name: "view_non_json_200_aborts", args: []string{"pr", "view", url5}, maskStderr: true,
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
	{name: "review_approve_redirect_is_failure", args: []string{"pr", "review", url5, "--approve"}, setup: func(s *bbfake.Server) {
		s.Fail("POST /approve", bbfake.Fault{Status: 302, Body: ""})
	}},
	{name: "review_approve_unreachable", args: []string{"pr", "review", url5, "--approve"}, closed: true},
	{name: "review_without_flag", args: []string{"pr", "review", url5}},
	{name: "review_other_flag", args: []string{"pr", "review", url5, "--request-changes"}},
	{name: "review_flag_checked_before_url", args: []string{"pr", "review", "bad", "--comment"}},
	{name: "review_bad_url", args: []string{"pr", "review", "bad", "--approve"}},
	{name: "review_no_arguments_unbound_variable", args: []string{"pr", "review"}},

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
	{name: "merge_post_unreachable_after_get", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /merge", bbfake.Fault{Drop: true}) }},
	{name: "merge_post_201_is_success", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /merge", bbfake.Fault{Status: 201, Body: "{}"}) }},
	{name: "merge_version_fetch_404", args: []string{"pr", "merge", "{BASE}/projects/PROJ/repos/repo/pull-requests/99"}},
	{name: "merge_version_fetch_unreachable", args: []string{"pr", "merge", url5}, closed: true},
	{name: "merge_version_missing_in_response", args: []string{"pr", "merge", url5}, setup: func(s *bbfake.Server) { s.Fail("GET /pull-requests/5", bbfake.Fault{Status: 200, Body: `{"id":5}`}) }},
	{name: "merge_bad_url", args: []string{"pr", "merge", "nope"}},
	{name: "merge_no_arguments_unbound_variable", args: []string{"pr", "merge"}},
	{name: "merge_slow_server_hits_max_time", args: []string{"pr", "merge", url5}, maxTime: "0.3",
		setup: func(s *bbfake.Server) {
			s.Fail("GET /pull-requests/5", bbfake.Fault{Delay: 10 * time.Second, Status: 200, Body: "{}"})
		}},

	// ---- pr comment ----
	{name: "comment_body", args: []string{"pr", "comment", url5, "--body", "LGTM"}, check: func(t *testing.T, r result) { eq(t, "stdout", r.raw, "") }},
	{name: "comment_body_is_json_escaped", args: []string{"pr", "comment", url5, "--body", "line one\nline \"two\" \\ ünï 🙂\t<b>&"}},
	{name: "comment_body_file_trailing_newlines_stripped", args: []string{"pr", "comment", url5, "--body-file", "{TMP}/body.txt"}, files: map[string]string{"body.txt": "first\n\nlast line\n\n\n"}},
	{name: "comment_body_file_missing", args: []string{"pr", "comment", url5, "--body-file", "{TMP}/nope.txt"}},
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
	{name: "comment_no_arguments", args: []string{"pr", "comment"}},
	{name: "comment_500", args: []string{"pr", "comment", url5, "--body", "x"}, setup: func(s *bbfake.Server) { s.Fail("POST /comments", bbfake.Fault{Status: 500, Body: "{}"}) }},
	{name: "comment_unreachable", args: []string{"pr", "comment", url5, "--body", "x"}, closed: true},

	// ---- pr close ----
	{name: "close_decline_only", args: []string{"pr", "close", url5}, check: func(t *testing.T, r result) { eq(t, "stdout", r.raw, "") }},
	{name: "close_comment_then_decline", args: []string{"pr", "close", url5, "--comment", "superseded by #6"}},
	{name: "close_comment_failure_aborts_decline", args: []string{"pr", "close", url5, "--comment", "x"}, setup: func(s *bbfake.Server) {
		s.Fail("POST /comments", bbfake.Fault{Status: 500, Body: "{}"})
	}},
	{name: "close_decline_409", args: []string{"pr", "close", url5}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 1 }},
	{name: "close_decline_is_not_retried_after_comment", args: []string{"pr", "close", url5, "--comment", "x"}, sc: func(sc *bbfake.Scenario) { pr5(sc).StaleDeclines = 1 }},
	{name: "close_decline_500_non_json", args: []string{"pr", "close", url5}, setup: func(s *bbfake.Server) { s.Fail("POST /decline", bbfake.Fault{Status: 500, Body: "oops"}) }},
	{name: "close_version_fetch_404", args: []string{"pr", "close", "{BASE}/projects/PROJ/repos/repo/pull-requests/99"}},
	{name: "close_version_missing_in_response", args: []string{"pr", "close", url5}, setup: func(s *bbfake.Server) { s.Fail("GET /pull-requests/5", bbfake.Fault{Status: 200, Body: "{}"}) }},
	{name: "close_comment_empty", args: []string{"pr", "close", url5, "--comment", ""}},
	{name: "close_comment_blank", args: []string{"pr", "close", url5, "--comment", " \t"}},
	{name: "close_comment_needs_value", args: []string{"pr", "close", url5, "--comment"}},
	{name: "close_unknown_argument", args: []string{"pr", "close", url5, "--force"}},
	{name: "close_bad_url", args: []string{"pr", "close", "bad"}},
	{name: "close_no_arguments", args: []string{"pr", "close"}},
	{name: "close_unreachable", args: []string{"pr", "close", url5}, closed: true},

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
	{name: "auth_token_needs_base_url", args: []string{"auth", "token"}, unset: []string{"BBAPPROVE_BASE_URL"}},
	{name: "auth_token_makes_no_requests", args: []string{"auth", "token", "extra", "args"}},

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
}

func TestAdapterCharacterization(t *testing.T) {
	bash, bin := toolDir(t)
	script, err := filepath.Abs("../../adapters/bitbucket")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range charCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc, bash, bin, script, string(src))
		})
	}
}

func runCase(t *testing.T, tc charCase, bash, bin, script, src string) {
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
	if tc.maxTime != "" {
		const orig = "CURL_MAX_TIME=3.5 "
		if !strings.Contains(src, orig) {
			t.Fatal("adapter no longer declares CURL_MAX_TIME=3.5; update the characterization test")
		}
		script = filepath.Join(tmp, "bitbucket-short-timeout")
		if err := os.WriteFile(script, []byte(strings.Replace(src, orig, "CURL_MAX_TIME="+tc.maxTime+" ", 1)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := bin
	opLog := filepath.Join(tmp, "op.log")
	if tc.op {
		opDir := filepath.Join(tmp, "opbin")
		if err := os.Mkdir(opDir, 0o700); err != nil {
			t.Fatal(err)
		}
		stub := "#!" + bash + "\n" + `echo "$*" >>"$OP_LOG"
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
		path = opDir + string(os.PathListSeparator) + bin
	}

	env := map[string]string{
		"PATH": path, "HOME": tmp, "LC_ALL": "C", "NO_PROXY": "*", "OP_LOG": opLog,
		"BBAPPROVE_BASE_URL": base, "BBAPPROVE_TOKEN": canary, "BBAPPROVE_RETRY_SLEEP": "0",
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
	cmd := exec.CommandContext(ctx, bash, append([]string{script}, args...)...)
	cmd.Dir, cmd.Env = tmp, environ
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
	if tc.maskStderr {
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
	checkGolden(t, tc.name, r)
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

func checkGolden(t *testing.T, name string, r result) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join("testdata", "adapter", name+".golden.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/bbfake -run Char -update)", err)
	}
	if bytes.Equal(want, buf.Bytes()) {
		return
	}
	// Distinguish a semantic change from formatting only (key order, whitespace) in stdout.
	var w result
	if json.Unmarshal(want, &w) == nil && w.Exit == r.Exit && w.Stderr == r.Stderr && reflect.DeepEqual(w.JSON, r.JSON) && w.Stdout != r.Stdout {
		t.Errorf("stdout is semantically equal JSON but its text differs (key order or whitespace)\n got: %s\nwant: %s", r.Stdout, w.Stdout)
		return
	}
	t.Errorf("%s differs from the golden file (run: go test ./internal/bbfake -run Char -update)\n--- got ---\n%s\n--- want ---\n%s", name, buf.Bytes(), want)
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
