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
)

// fakeAdapter writes a /bin/sh script with the given body into a temp dir and returns its path.
func fakeAdapter(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "adapter")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// newClient keeps PATH so the fake script can call sleep/cat, and nothing else from the host.
func newClient(path string) *Client {
	return &Client{Path: path, Token: "test-token", Environ: []string{"PATH=" + os.Getenv("PATH")}}
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
			ad := fakeAdapter(t, "cat <<'EOF'\n"+tc.json+"\nEOF")
			got, err := newClient(ad).List(context.Background())
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
	ad := fakeAdapter(t, `echo "boom: bad things" >&2; exit 3`)
	_, err := newClient(ad).List(context.Background())
	if err == nil {
		t.Fatal("List: expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom: bad things") {
		t.Fatalf("error %q does not contain adapter stderr", err)
	}
}

func TestPerCallTimeout(t *testing.T) {
	// exec replaces the shell so the timeout kills sleep directly and the pipes close promptly.
	ad := fakeAdapter(t, "exec sleep 5")
	c := newClient(ad)
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
	ad := fakeAdapter(t, `if [ "$1" = pr ] && [ "$2" = merge ] && [ "$3" = https://bb/pr/9 ]; then exit 0; fi; echo "bad args: $*" >&2; exit 9`)
	if err := newClient(ad).Merge(context.Background(), "https://bb/pr/9"); err != nil {
		t.Fatalf("Merge: unexpected error: %v", err)
	}
}

// withAuthor sets the nested Author fields, whose anonymous struct type carries json tags.
func withAuthor(p PR, email, login string) PR {
	p.Author.Email, p.Author.Login = email, login
	return p
}

// argvAdapter logs each argv entry on its own line, NUL-free, followed by a "--" separator, to log.
func argvAdapter(t *testing.T, exit int, stderr string) (path, log string) {
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
			ad, log := argvAdapter(t, 0, "")
			if err := tc.call(newClient(ad)); err != nil {
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
	ad := fakeAdapter(t, `[ "$#" = 5 ] && [ "$5" = "$(printf 'x\ny')" ] || { echo "argc=$#" >&2; exit 4; }`)
	if err := newClient(ad).Comment(context.Background(), "u", "x\ny"); err != nil {
		t.Fatal(err)
	}
}

func TestCommentCloseStderrPropagation(t *testing.T) {
	ad, _ := argvAdapter(t, 5, "comment rejected: 403")
	c := newClient(ad)
	if err := c.Comment(context.Background(), "u", "b"); err == nil || !strings.Contains(err.Error(), "comment rejected: 403") {
		t.Fatalf("Comment err = %v", err)
	}
	if err := c.Close(context.Background(), "u", "b"); err == nil || !strings.Contains(err.Error(), "comment rejected: 403") {
		t.Fatalf("Close err = %v", err)
	}
}

func TestAuthToken(t *testing.T) {
	c := newClient(fakeAdapter(t, `[ "$1 $2" = "auth token" ] && echo "  tok-from-item  "`))
	c.Token = ""
	if got, err := c.AuthToken(context.Background()); err != nil || got != "tok-from-item" {
		t.Errorf("got %q, %v", got, err)
	}
	c = newClient(fakeAdapter(t, `echo "No token: set BBAPPROVE_TOKEN" >&2; exit 1`))
	if _, err := c.AuthToken(context.Background()); err == nil || !strings.Contains(err.Error(), "No token") {
		t.Errorf("want the adapter's stderr as error, got %v", err)
	}
	c = newClient(fakeAdapter(t, `:`))
	if _, err := c.AuthToken(context.Background()); err == nil {
		t.Error("an empty token must be an error")
	}
}
