package adapter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bbapprove/internal/testfix"
)

// fakeAdapter returns a client whose adapter is the shared fixed script (written once per test
// binary) running body, a /bin/sh snippet, via FAKE_BODY. PATH is kept so the snippet can call
// sleep/cat, and nothing else from the host.
func fakeAdapter(t *testing.T, body string) *Client {
	t.Helper()
	testfix.RequireShell(t)
	return &Client{Path: testfix.Adapter(), Token: "test-token",
		Environ: []string{"PATH=" + os.Getenv("PATH"), "FAKE_BODY=" + body}}
}

func TestListParsesNormalizedPRs(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []PR
	}{
		{
			name: "full object",
			json: `[{"url":"https://bb/pr/7","number":7,"project":"PRJ","repo":"repo","title":"chore: bump",
				"author":{"email":"a@b.c","login":"bot"},"headBranch":"feat/x","approvedByMe":true,
				"build":"passed","qualityGate":"failed","approvals":2,"needsWork":1,"commentCount":3,"openTasks":4}]`,
			want: []PR{withAuthor(PR{
				URL: "https://bb/pr/7", Number: 7, Project: "PRJ", Repo: "repo", Title: "chore: bump",
				HeadBranch:   "feat/x",
				ApprovedByMe: true,
				Build:        "passed",
				QualityGate:  "failed",
				Approvals:    2,
				NeedsWork:    1,
				CommentCount: 3,
				OpenTasks:    4,
			}, "a@b.c", "bot")},
		},
		{
			name: "missing optional fields decode to zero values",
			json: `[{"url":"https://bb/pr/1","number":1,"title":"t"}]`,
			want: []PR{{URL: "https://bb/pr/1", Number: 1, Title: "t"}},
		},
		{
			name: "empty array",
			json: `[]`,
			want: []PR{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeAdapter(t, "cat <<'EOF'\n"+tc.json+"\nEOF")
			got, err := c.List(context.Background())
			if err != nil {
				t.Fatalf("List: unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("List = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNonZeroExitReturnsStderr(t *testing.T) {
	c := fakeAdapter(t, `echo "boom: bad things" >&2; exit 3`)
	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("List: expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom: bad things") {
		t.Fatalf("error %q does not contain adapter stderr", err)
	}
}

func TestPerCallTimeout(t *testing.T) {
	// exec replaces the shell so the timeout kills sleep directly and the pipes close promptly.
	c := fakeAdapter(t, "exec sleep 5")
	c.Timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := c.List(context.Background())
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("List error = %v, want timeout error", err)
	}
	if elapsed > time.Second {
		t.Fatalf("List took %s, want well under 1s", elapsed)
	}
}

func TestMergeTimeoutSelection(t *testing.T) {
	if MergeTimeout != 60*time.Second {
		t.Fatalf("MergeTimeout = %s, want 60s", MergeTimeout)
	}
	tests := []struct {
		name    string
		timeout time.Duration // Client.Timeout override
		def     time.Duration
		want    time.Duration
	}{
		{"merge uses MergeTimeout by default", 0, MergeTimeout, MergeTimeout},
		{"other calls use DefaultTimeout by default", 0, DefaultTimeout, DefaultTimeout},
		{"Client.Timeout overrides the per-call default", 5 * time.Millisecond, MergeTimeout, 5 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{Timeout: tc.timeout}
			if got := c.timeout(tc.def); got != tc.want {
				t.Fatalf("timeout(%s) = %s, want %s", tc.def, got, tc.want)
			}
		})
	}
}

func TestMergeRunsPRMerge(t *testing.T) {
	c := fakeAdapter(t, `if [ "$1" = pr ] && [ "$2" = merge ] && [ "$3" = https://bb/pr/9 ]; then exit 0; fi; echo "bad args: $*" >&2; exit 9`)
	if err := c.Merge(context.Background(), "https://bb/pr/9"); err != nil {
		t.Fatalf("Merge: unexpected error: %v", err)
	}
}

// withAuthor sets the nested Author fields, whose anonymous struct type carries json tags.
func withAuthor(p PR, email, login string) PR {
	p.Author.Email, p.Author.Login = email, login
	return p
}

// argvAdapter logs each argv entry on its own line, NUL-free, followed by a "--" separator, to log.
func argvAdapter(t *testing.T, exit int, stderr string) (c *Client, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "argv.log")
	body := `for a in "$@"; do printf '%s\n' "$a" >> "` + log + `"; done; printf -- '--\n' >> "` + log + `"`
	if stderr != "" {
		body += "; echo '" + stderr + "' >&2"
	}
	body += "; exit " + strconv.Itoa(exit)
	return fakeAdapter(t, body), log
}

func TestCommentAndCloseArgv(t *testing.T) {
	const url = "https://bb/pr/9"
	tests := []struct {
		name string
		call func(c *Client) error
		want []string
	}{
		{"comment simple", func(c *Client) error { return c.Comment(context.Background(), url, "hello") },
			[]string{"pr", "comment", url, "--body", "hello"}},
		{"comment special chars", func(c *Client) error {
			return c.Comment(context.Background(), url, `a "b" $HOME 'c' ; rm -rf * --flag`)
		},
			[]string{"pr", "comment", url, "--body", `a "b" $HOME 'c' ; rm -rf * --flag`}},
		{"comment newline", func(c *Client) error { return c.Comment(context.Background(), url, "line1\nline2") },
			[]string{"pr", "comment", url, "--body", "line1", "line2"}}, // log is line-based; newline splits entries
		{"close no comment", func(c *Client) error { return c.Close(context.Background(), url, "") },
			[]string{"pr", "close", url}},
		{"close with comment", func(c *Client) error { return c.Close(context.Background(), url, "superseded by #10; $x") },
			[]string{"pr", "close", url, "--comment", "superseded by #10; $x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, log := argvAdapter(t, 0, "")
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(log)
			got := strings.Split(strings.TrimSuffix(string(b), "--\n"), "\n")
			got = got[:len(got)-1]
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommentNewlineIsOneArg(t *testing.T) {
	c := fakeAdapter(t, `[ "$#" = 5 ] && [ "$5" = "$(printf 'x\ny')" ] || { echo "argc=$#" >&2; exit 4; }`)
	if err := c.Comment(context.Background(), "u", "x\ny"); err != nil {
		t.Fatal(err)
	}
}

func TestCommentCloseStderrPropagation(t *testing.T) {
	c, _ := argvAdapter(t, 5, "comment rejected: 403")
	if err := c.Comment(context.Background(), "u", "b"); err == nil || !strings.Contains(err.Error(), "comment rejected: 403") {
		t.Fatalf("Comment err = %v", err)
	}
	if err := c.Close(context.Background(), "u", "b"); err == nil || !strings.Contains(err.Error(), "comment rejected: 403") {
		t.Fatalf("Close err = %v", err)
	}
}

func TestAuthToken(t *testing.T) {
	c := fakeAdapter(t, `[ "$1 $2" = "auth token" ] && echo "  tok-from-item  "`)
	c.Token = ""
	if got, err := c.AuthToken(context.Background()); err != nil || got != "tok-from-item" {
		t.Errorf("got %q, %v", got, err)
	}
	c = fakeAdapter(t, `echo "No token: set BBAPPROVE_TOKEN" >&2; exit 1`)
	if _, err := c.AuthToken(context.Background()); err == nil || !strings.Contains(err.Error(), "No token") {
		t.Errorf("want the adapter's stderr as error, got %v", err)
	}
	c = fakeAdapter(t, `:`)
	if _, err := c.AuthToken(context.Background()); err == nil {
		t.Error("an empty token must be an error")
	}
}

func TestMergeWarnReturnsStderrOfASuccessfulMerge(t *testing.T) {
	c := fakeAdapter(t, `printf 'first warning\n\n  second warning  \n' >&2; exit 0`)
	warns, err := c.MergeWarn(context.Background(), "https://bb/pr/9")
	if err != nil || !reflect.DeepEqual(warns, []string{"first warning", "second warning"}) {
		t.Fatalf("warns %q err %v", warns, err)
	}
	if err := c.Merge(context.Background(), "https://bb/pr/9"); err != nil {
		t.Fatalf("Merge must ignore warnings: %v", err)
	}
}

func TestMergeWarnSilentAdapterAndFailure(t *testing.T) {
	if warns, err := fakeAdapter(t, `exit 0`).MergeWarn(context.Background(), "u"); err != nil || len(warns) != 0 {
		t.Fatalf("silent adapter: %q %v", warns, err)
	}
	warns, err := fakeAdapter(t, `echo "a warning" >&2; echo "merge HTTP 409" >&2; exit 1`).MergeWarn(context.Background(), "u")
	if err == nil || len(warns) != 0 || !strings.Contains(err.Error(), "merge HTTP 409") {
		t.Fatalf("failure must be an error without warnings: %q %v", warns, err)
	}
}
