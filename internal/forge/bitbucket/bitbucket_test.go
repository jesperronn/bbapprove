package bitbucket_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"bbapprove/internal/bbfake"
	"bbapprove/internal/forge/bitbucket"
)

const canary = "CANARY-TOKEN-do-not-print-0123456789"

func scenario() *bbfake.Scenario {
	return &bbfake.Scenario{
		Token: canary, Me: "me",
		PRs: []*bbfake.PR{{
			Project: "PROJ", Repo: "repo", ID: 5, Version: 3, Title: "chore(deps): update x",
			AuthorEmail: "renovate-bot@example.com", AuthorSlug: "renovate-bot", Branch: "renovate/x", Commit: "abc",
			Reviewers: []bbfake.Person{{Slug: "me", Status: "APPROVED"}, {Slug: "bob", Status: "NEEDS_WORK"}},
			QGStatus:  "PASSED", CommentCount: 4, OpenTasks: 1,
			CommentedActivities: 3, Tasks: []string{"OPEN", "RESOLVED"},
		}},
		Builds: map[string]bbfake.BuildStats{"abc": {Successful: 2}},
	}
}

type outcome struct {
	code           int
	stdout, stderr string
}

// run executes one command line against a fresh fake and returns what the CLI would print.
func run(t *testing.T, sc *bbfake.Scenario, setup func(*bbfake.Server, *bitbucket.Env), args ...string) (outcome, *bbfake.Server) {
	t.Helper()
	srv := bbfake.New(sc)
	t.Cleanup(srv.Close)
	env := bitbucket.Env{BaseURL: srv.URL(), Token: canary, Sleep: func(time.Duration) {}, MaxTime: 2 * time.Second}
	if setup != nil {
		setup(srv, &env)
	}
	var out, errb bytes.Buffer
	code := bitbucket.Run(context.Background(), args, env, &out, &errb)
	return outcome{code, out.String(), errb.String()}, srv
}

func pr5url(s *bbfake.Server) string { return s.URL() + "/projects/PROJ/repos/repo/pull-requests/5" }

func TestMarshalPRKeyOrderAndEscapes(t *testing.T) {
	t.Parallel()
	p := bitbucket.PR{
		URL: "http://h/projects/P/repos/r/pull-requests/1", Number: 1, Project: "P", Repo: "r", Title: "a\"b\\c\n\t\x01\x7f <&> é",
		AuthorEmail: "e@example.com", AuthorLogin: "l", HeadBranch: "b", ApprovedByMe: true, Build: "passed", QualityGate: "unknown",
		Approvals: 2, NeedsWork: 1, CommentCount: 3, OpenTasks: 4,
	}
	want := `{"url":"http://h/projects/P/repos/r/pull-requests/1","number":1,"project":"P","repo":"r","title":"a\"b\\c\n\t\u0001\u007f <&> é",` +
		`"author":{"email":"e@example.com","login":"l"},"headBranch":"b","approvedByMe":true,"build":"passed","qualityGate":"unknown",` +
		`"approvals":2,"needsWork":1,"commentCount":3,"openTasks":4}`
	if got := string(bitbucket.MarshalPR(p)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := string(bitbucket.MarshalPRs(nil)); got != "[]" {
		t.Errorf("no PRs: %s", got)
	}
}

func TestNormalizationRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		tweak  func(*bbfake.Scenario)
		build  string
		gate   string
		approv bool
	}{
		{"failed beats running and passed", func(s *bbfake.Scenario) { s.Builds["abc"] = bbfake.BuildStats{Successful: 9, Failed: 1, InProgress: 2} }, "failed", "passed", true},
		{"running beats passed", func(s *bbfake.Scenario) { s.Builds["abc"] = bbfake.BuildStats{Successful: 9, InProgress: 1} }, "running", "passed", true},
		{"no stats entry", func(s *bbfake.Scenario) { s.Builds = nil }, "none", "passed", true},
		{"gate is case-insensitive and unknown otherwise", func(s *bbfake.Scenario) { s.PRs[0].QGStatus = "FaIlEd" }, "passed", "failed", true},
		{"gate INPROGRESS is unknown", func(s *bbfake.Scenario) { s.PRs[0].QGStatus = "INPROGRESS" }, "passed", "unknown", true},
		{"participant approval counts for me", func(s *bbfake.Scenario) {
			s.PRs[0].Reviewers = nil
			s.PRs[0].Participants = []bbfake.Person{{Slug: "me", Status: "APPROVED"}}
		}, "passed", "passed", true},
		{"my status UNAPPROVED is not approved", func(s *bbfake.Scenario) { s.PRs[0].Reviewers[0].Status = "UNAPPROVED" }, "passed", "passed", false},
		{"nobody logged in", func(s *bbfake.Scenario) { s.Me = "" }, "passed", "passed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := scenario()
			tc.tweak(sc)
			srv := bbfake.New(sc)
			defer srv.Close()
			c, err := bitbucket.NewClient(bitbucket.Env{BaseURL: srv.URL(), Token: canary})
			if err != nil {
				t.Fatal(err)
			}
			prs, err := c.ListPRs(context.Background())
			if err != nil || len(prs) != 1 {
				t.Fatalf("ListPRs: %v %v", prs, err)
			}
			p := prs[0]
			if p.Build != tc.build || p.QualityGate != tc.gate || p.ApprovedByMe != tc.approv {
				t.Errorf("build=%s gate=%s approved=%v, want %s %s %v", p.Build, p.QualityGate, p.ApprovedByMe, tc.build, tc.gate, tc.approv)
			}
		})
	}
}

func TestViewCommentCountAndTasks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		commented, task int
		tasks           []string
		pageSize        int
		wantComments    int
		wantOpen        int
	}{
		{"tasks are subtracted", 3, 0, []string{"OPEN", "RESOLVED"}, 0, 1, 1},
		{"clamped at zero", 0, 0, []string{"OPEN"}, 0, 0, 1},
		{"paged", 7, 0, []string{"OPEN", "OPEN", "RESOLVED"}, 2, 4, 2},
		{"cap of 50 pages counts what was read", 130, 0, nil, 2, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := scenario()
			sc.ItemPageSize = tc.pageSize
			sc.PRs[0].CommentedActivities, sc.PRs[0].OtherActivities, sc.PRs[0].Tasks = tc.commented, 0, tc.tasks
			srv := bbfake.New(sc)
			defer srv.Close()
			c, _ := bitbucket.NewClient(bitbucket.Env{BaseURL: srv.URL(), Token: canary})
			p, err := c.ViewPR(context.Background(), pr5url(srv))
			if err != nil {
				t.Fatal(err)
			}
			if p.CommentCount != tc.wantComments || p.OpenTasks != tc.wantOpen {
				t.Errorf("commentCount=%d openTasks=%d, want %d %d", p.CommentCount, p.OpenTasks, tc.wantComments, tc.wantOpen)
			}
			if p.QualityGate != "unknown" {
				t.Errorf("qualityGate = %q: single-PR view stays unknown (see TODO in ViewPR)", p.QualityGate)
			}
		})
	}
}

func TestWhoamiIsTrimmedAtBothEndsOnly(t *testing.T) {
	t.Parallel()
	// The only reviewer has the slug "m e": a reply of "m e" (inner space kept) approves, " me \r\n" is "me".
	for _, tc := range []struct {
		body string
		want bool
	}{{" m e \r\n", true}, {" me \r\n", false}} {
		sc := scenario()
		sc.PRs[0].Reviewers = []bbfake.Person{{Slug: "m e", Status: "APPROVED"}}
		o, _ := run(t, sc, func(s *bbfake.Server, _ *bitbucket.Env) {
			s.Fail("GET /whoami", bbfake.Fault{Status: 200, Body: tc.body})
		}, "pr", "list", "--reviewer", "@me", "--state", "open")
		if got := strings.Contains(o.stdout, `"approvedByMe":true`); got != tc.want {
			t.Errorf("whoami %q: approvedByMe=%v (stdout %s)", tc.body, got, o.stdout)
		}
	}
}

func TestMergeRetryPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		retries      string
		stale        int
		wantExit     int
		wantAttempts int
	}{
		{"default is 4 attempts", "", 99, 1, 4},
		{"succeeds after two stale versions", "", 2, 0, 3},
		{"0 still tries once", "0", 1, 1, 1},
		{"1 means no retry", "1", 1, 1, 1},
		{"7 attempts", "7", 99, 1, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := scenario()
			sc.PRs[0].StaleMerges = tc.stale
			o, srv := run(t, sc, func(_ *bbfake.Server, e *bitbucket.Env) { e.MergeRetries = tc.retries }, "pr", "merge", "x/projects/PROJ/repos/repo/pull-requests/5")
			if o.code != tc.wantExit {
				t.Errorf("exit %d, want %d (stderr %q)", o.code, tc.wantExit, o.stderr)
			}
			posts := 0
			for _, r := range srv.Requests() {
				if r.Method == "POST" {
					posts++
				}
			}
			if posts != tc.wantAttempts {
				t.Errorf("%d merge attempts, want %d", posts, tc.wantAttempts)
			}
		})
	}
}

func TestRetrySleepIsUsedBetweenAttempts(t *testing.T) {
	t.Parallel()
	sc := scenario()
	sc.PRs[0].StaleMerges = 2
	var slept []time.Duration
	o, _ := run(t, sc, func(_ *bbfake.Server, e *bitbucket.Env) {
		e.RetrySleep = "3"
		e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	}, "pr", "merge", "x/projects/PROJ/repos/repo/pull-requests/5")
	if o.code != 0 || len(slept) != 2 || slept[0] != 3*time.Second {
		t.Errorf("exit %d, sleeps %v", o.code, slept)
	}
}

func TestEnvValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		env     func(*bitbucket.Env)
		args    []string
		want    int
		message string
	}{
		{"retries text", func(e *bitbucket.Env) { e.MergeRetries = "x" }, []string{"pr", "merge", "/projects/P/repos/r/pull-requests/5"}, 2, "BBAPPROVE_MERGE_RETRIES must be a non-negative integer"},
		{"retries negative", func(e *bitbucket.Env) { e.MergeRetries = "-1" }, []string{"pr", "close", "/projects/P/repos/r/pull-requests/5"}, 2, "BBAPPROVE_MERGE_RETRIES"},
		{"sleep decimal", func(e *bitbucket.Env) { e.RetrySleep = "0.5" }, []string{"pr", "merge", "/projects/P/repos/r/pull-requests/5"}, 2, "BBAPPROVE_RETRY_SLEEP"},
		{"no base url", func(e *bitbucket.Env) { e.BaseURL = "///" }, []string{"pr", "list"}, 2, "BBAPPROVE_BASE_URL is not set"},
		{"no token", func(e *bitbucket.Env) { e.Token = "" }, []string{"pr", "list"}, 2, "BBAPPROVE_TOKEN is not set"},
		{"no token beats usage", func(e *bitbucket.Env) { e.Token = "" }, []string{"bogus"}, 2, "BBAPPROVE_TOKEN is not set"},
		{"usage", func(*bitbucket.Env) {}, []string{"pr"}, 2, "usage: bitbucket"},
		{"usage no args", func(*bitbucket.Env) {}, nil, 2, "usage: bitbucket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := bitbucket.Env{BaseURL: "http://127.0.0.1:1", Token: canary}
			tc.env(&env)
			var out, errb bytes.Buffer
			code := bitbucket.Run(context.Background(), tc.args, env, &out, &errb)
			if code != tc.want || !strings.Contains(errb.String(), tc.message) || out.Len() != 0 {
				t.Errorf("exit %d stdout %q stderr %q, want exit %d containing %q", code, out.String(), errb.String(), tc.want, tc.message)
			}
		})
	}
}

func TestBaseURLTrailingSlashesAreTrimmed(t *testing.T) {
	t.Parallel()
	o, srv := run(t, scenario(), func(s *bbfake.Server, e *bitbucket.Env) { e.BaseURL += "//" }, "pr", "list", "--reviewer", "@me", "--state", "open")
	if o.code != 0 || !strings.Contains(o.stdout, srv.URL()+"/projects/PROJ/repos/repo/pull-requests/5") {
		t.Errorf("exit %d stdout %s stderr %s", o.code, o.stdout, o.stderr)
	}
}

func TestListPagesThroughEverything(t *testing.T) {
	t.Parallel()
	sc := scenario()
	sc.PageSize = 1
	for i := 6; i <= 8; i++ {
		sc.PRs = append(sc.PRs, &bbfake.PR{Project: "PROJ", Repo: "repo", ID: i, Version: 1, Title: "t", Commit: fmt.Sprint("c", i)})
	}
	o, srv := run(t, sc, nil, "pr", "list", "--reviewer", "@me", "--state", "open")
	if o.code != 0 || strings.Count(o.stdout, `"number"`) != 4 {
		t.Fatalf("exit %d stdout %s stderr %s", o.code, o.stdout, o.stderr)
	}
	var stats int
	for _, r := range srv.Requests() {
		if r.Path == "/rest/build-status/1.0/commits/stats" {
			stats++
			if r.Body != `["abc","c6","c7","c8"]` {
				t.Errorf("stats body %s: one POST with every sha", r.Body)
			}
		}
	}
	if stats != 1 {
		t.Errorf("%d stats requests, want 1", stats)
	}
}

func TestCloseCommentFirstAndCommentStaysWhenDeclineFails(t *testing.T) {
	t.Parallel()
	sc := scenario()
	sc.PRs[0].StaleDeclines = 99
	o, srv := run(t, sc, nil, "pr", "close", "x/projects/PROJ/repos/repo/pull-requests/5", "--comment", "superseded")
	if o.code != 1 || len(srv.Comments(5)) != 1 {
		t.Errorf("exit %d, comments %v, stderr %s", o.code, srv.Comments(5), o.stderr)
	}
	if first := srv.Requests()[0]; first.Method != "POST" || !strings.HasSuffix(first.Path, "/comments") {
		t.Errorf("the comment must be the first request, got %s %s", first.Method, first.Path)
	}
}

func TestCommentBodyFileFromStdin(t *testing.T) {
	t.Parallel()
	o, srv := run(t, scenario(), func(_ *bbfake.Server, e *bitbucket.Env) { e.Stdin = strings.NewReader("line\n\n") },
		"pr", "comment", "x/projects/PROJ/repos/repo/pull-requests/5", "--body-file", "-")
	if got := srv.Comments(5); o.code != 0 || len(got) != 1 || got[0] != "line" {
		t.Errorf("exit %d comments %q stderr %s", o.code, got, o.stderr)
	}
}

func TestTransportFailureMessages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fault bbfake.Fault
		env   func(*bitbucket.Env)
		want  string
	}{
		{"dropped", bbfake.Fault{Drop: true}, nil, "POST /rest/api/1.0/projects/PROJ/repos/repo/pull-requests/5/approve: connection failed"},
		{"slow", bbfake.Fault{Delay: 5 * time.Second, Status: 200}, func(e *bitbucket.Env) { e.MaxTime = 150 * time.Millisecond }, "approve: timed out after 0.15s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, _ := run(t, scenario(), func(s *bbfake.Server, e *bitbucket.Env) {
				s.Fail("POST /approve", tc.fault)
				if tc.env != nil {
					tc.env(e)
				}
			}, "pr", "review", "x/projects/PROJ/repos/repo/pull-requests/5", "--approve")
			if o.code != 1 || !strings.Contains(o.stderr, tc.want) || strings.Contains(o.stderr, "curl") {
				t.Errorf("exit %d stderr %q, want it to contain %q", o.code, o.stderr, tc.want)
			}
		})
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	o, srv := run(t, scenario(), func(s *bbfake.Server, _ *bitbucket.Env) {
		s.Fail("POST /approve", bbfake.Fault{Status: 307, Headers: map[string]string{"Location": "https://login.example.com/"}})
	}, "pr", "review", "x/projects/PROJ/repos/repo/pull-requests/5", "--approve")
	want := "redirect to https://login.example.com/; check BBAPPROVE_BASE_URL / repository\n"
	if o.code != 1 || o.stderr != want || len(srv.Requests()) != 1 {
		t.Errorf("exit %d stderr %q requests %d", o.code, o.stderr, len(srv.Requests()))
	}
}

func TestRequestHeaders(t *testing.T) {
	t.Parallel()
	_, srv := run(t, scenario(), nil, "pr", "comment", "x/projects/PROJ/repos/repo/pull-requests/5", "--body", "hi")
	for _, r := range srv.Requests() {
		if r.Authorization != "Bearer "+canary || r.Accept != "application/json" {
			t.Errorf("%s %s: authorization/accept = %q/%q", r.Method, r.Path, r.Authorization, r.Accept)
		}
		if (r.Method == "POST") != (r.ContentType == "application/json") {
			t.Errorf("%s %s: Content-Type %q only on bodies", r.Method, r.Path, r.ContentType)
		}
	}
}

func TestAuthToken(t *testing.T) {
	t.Parallel()
	opOK := func(fields map[string]string) func(context.Context, string, []string) (string, error) {
		return func(_ context.Context, _ string, args []string) (string, error) {
			v, ok := fields[strings.TrimPrefix(args[len(args)-1], "label=")]
			if !ok {
				return "", errors.New("no field")
			}
			return v + "\n\n", nil
		}
	}
	found := func(string) (string, error) { return "/usr/bin/op", nil }
	for _, tc := range []struct {
		name       string
		env        bitbucket.Env
		wantTok    string
		wantErr    string
		wantNotice string
	}{
		{"env wins, op not called", bitbucket.Env{Token: "T", TokenName: "ITEM", RunOp: func(context.Context, string, []string) (string, error) { panic("op called") }}, "T", "", ""},
		{"token field", bitbucket.Env{TokenName: "ITEM", LookPath: found, RunOp: opOK(map[string]string{"token": "A", "credential": "B"})}, "A", "", "Waiting for 1Password: op item get ITEM\n"},
		{"credential fallback", bitbucket.Env{TokenName: "ITEM", LookPath: found, RunOp: opOK(map[string]string{"credential": "B"})}, "B", "", "Waiting for 1Password: op item get ITEM\n"},
		{"dim on a tty", bitbucket.Env{TokenName: "ITEM", StderrIsTTY: true, LookPath: found, RunOp: opOK(map[string]string{"token": "A"})}, "A", "", "\x1b[2mWaiting for 1Password: op item get ITEM\x1b[0m\n"},
		{"dim under FORCE_COLOR", bitbucket.Env{TokenName: "ITEM", ForceColor: "1", LookPath: found, RunOp: opOK(map[string]string{"token": "A"})}, "A", "", "\x1b[2mWaiting for 1Password: op item get ITEM\x1b[0m\n"},
		{"NO_COLOR wins", bitbucket.Env{TokenName: "ITEM", NoColor: "1", ForceColor: "1", StderrIsTTY: true, LookPath: found, RunOp: opOK(map[string]string{"token": "A"})}, "A", "", "Waiting for 1Password: op item get ITEM\n"},
		{"both fields fail", bitbucket.Env{TokenName: "ITEM", LookPath: found, RunOp: opOK(nil)}, "", "No token: set BBAPPROVE_TOKEN", "Waiting for 1Password: op item get ITEM\n"},
		{"op missing", bitbucket.Env{TokenName: "ITEM", LookPath: func(string) (string, error) { return "", errors.New("nope") }}, "", "1Password CLI (op) not found", ""},
		{"nothing configured", bitbucket.Env{}, "", "No token: set BBAPPROVE_TOKEN", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var errb bytes.Buffer
			tok, err := bitbucket.AuthToken(context.Background(), tc.env, &errb)
			if tok != tc.wantTok || (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("token %q err %v, want %q / %q", tok, err, tc.wantTok, tc.wantErr)
			}
			if errb.String() != tc.wantNotice {
				t.Errorf("stderr %q, want %q", errb.String(), tc.wantNotice)
			}
		})
	}
}

// The token may appear on stdout only for `auth token`; never on stderr and never in a log line.
// Not parallel: it redirects the global logger.
func TestTokenNeverLeaks(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(io.Discard)

	type cmd struct {
		args  []string
		setup func(*bbfake.Server)
	}
	u := "x/projects/PROJ/repos/repo/pull-requests/5"
	cmds := []cmd{
		{args: []string{"pr", "list", "--reviewer", "@me", "--state", "open"}},
		{args: []string{"pr", "view", u}},
		{args: []string{"pr", "review", u, "--approve"}},
		{args: []string{"pr", "merge", u}},
		{args: []string{"pr", "comment", u, "--body", "hi"}},
		{args: []string{"pr", "close", u, "--comment", "bye"}},
		{args: []string{"pr", "list", "--reviewer", "@me", "--state", "open"}, setup: func(s *bbfake.Server) { s.Fail("GET /dashboard/pull-requests", bbfake.Fault{Status: 401, Body: "{}"}) }},
		{args: []string{"pr", "merge", u}, setup: func(s *bbfake.Server) { s.Fail("POST /merge", bbfake.Fault{Drop: true}) }},
		{args: []string{"pr", "review", u, "--approve"}, setup: func(s *bbfake.Server) { s.Fail("POST /approve", bbfake.Fault{Status: 500, Body: "Bearer " + "oops"}) }},
		{args: []string{"pr", "view", "nonsense"}},
		{args: []string{"pr", "bogus"}},
	}
	for _, c := range cmds {
		sc := scenario()
		srv := bbfake.New(sc)
		if c.setup != nil {
			c.setup(srv)
		}
		var out, errb bytes.Buffer
		env := bitbucket.Env{BaseURL: srv.URL(), Token: canary, Sleep: func(time.Duration) {}}
		bitbucket.Run(context.Background(), c.args, env, &out, &errb)
		srv.Close()
		for what, s := range map[string]string{"stdout": out.String(), "stderr": errb.String(), "log": logs.String()} {
			if strings.Contains(s, canary) {
				t.Errorf("%v: the token leaked into %s", c.args, what)
			}
		}
	}
	// auth token prints it on stdout, and only there.
	var out, errb bytes.Buffer
	if code := bitbucket.Run(context.Background(), []string{"auth", "token"}, bitbucket.Env{Token: canary}, &out, &errb); code != 0 || out.String() != canary+"\n" || errb.Len() != 0 {
		t.Errorf("auth token: exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
}
