// Package adapter wraps the gh-style forge adapter CLI. It is the only way the Go code talks to the forge.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"bbapprove/internal/forge/bitbucket"
)

// DefaultTimeout bounds every adapter call.
const DefaultTimeout = 10 * time.Second

// MergeTimeout bounds `pr merge`, which retries internally and can legitimately take ~35s.
const MergeTimeout = 60 * time.Second

// PR is the normalized pull request the adapter emits. Missing optional fields decode to zero values.
type PR struct {
	URL     string `json:"url"`
	Number  int    `json:"number"`
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Title   string `json:"title"`
	Author  struct {
		Email string `json:"email"`
		Login string `json:"login"`
	} `json:"author"`
	HeadBranch   string `json:"headBranch"`
	ApprovedByMe bool   `json:"approvedByMe"`
	Build        string `json:"build"`       // passed|failed|running|none
	QualityGate  string `json:"qualityGate"` // passed|failed|unknown
	Approvals    int    `json:"approvals"`
	NeedsWork    int    `json:"needsWork"`
	CommentCount int    `json:"commentCount"`
	OpenTasks    int    `json:"openTasks"`
}

// RunFunc runs one adapter command line (args without the program name) in this process and returns the
// adapter's exit code. environ is the KEY=VALUE environment the adapter would get as a child process.
type RunFunc func(ctx context.Context, args, environ []string, stdout, stderr io.Writer) int

// BuiltIn returns the in-process adapter for forge type typ, or nil when there is none (the type is
// served by an executable, adapters/<type>). Only "bitbucket" is built in.
func BuiltIn(typ string) RunFunc {
	if typ != "bitbucket" {
		return nil
	}
	return func(ctx context.Context, args, environ []string, stdout, stderr io.Writer) int {
		return bitbucket.Run(ctx, args, bitbucket.EnvFromEnviron(environ), stdout, stderr)
	}
}

// Client runs the adapter: in this process when InProcess is set, else the executable at Path as a subprocess.
type Client struct {
	Path      string
	InProcess RunFunc // the built-in adapter (see BuiltIn); nil means run Path
	Token     string  // passed to the adapter as BBAPPROVE_TOKEN when set
	Environ   []string
	Timeout   time.Duration
}

// timeout returns c.Timeout when set, otherwise def.
func (c *Client) timeout(def time.Duration) time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return def
}

// run executes the adapter with DefaultTimeout and returns trimmed stdout. On failure the error text is the adapter's stderr.
func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	out, _, err := c.runFor(ctx, DefaultTimeout, args...)
	return out, err
}

// runFor is run with an explicit per-call timeout (overridden by c.Timeout when set). It also returns the
// trimmed stderr of a successful run, where an adapter reports a warning without failing.
func (c *Client) runFor(ctx context.Context, def time.Duration, args ...string) (stdout, stderr string, err error) {
	t := c.timeout(def)
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	environ := append(append([]string{}, c.Environ...), "BBAPPROVE_TOKEN="+c.Token)
	var out, errb bytes.Buffer
	var runErr error
	if c.InProcess != nil {
		if code := c.InProcess(ctx, args, environ, &out, &errb); code != 0 {
			runErr = fmt.Errorf("adapter exited with status %d", code)
		}
	} else {
		cmd := exec.CommandContext(ctx, c.Path, args...)
		cmd.Env = environ
		cmd.WaitDelay = time.Second
		cmd.Stdout, cmd.Stderr = &out, &errb
		runErr = cmd.Run()
	}
	if err := runErr; err != nil {
		msg := strings.TrimSpace(errb.String())
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("adapter %s timed out after %s", strings.Join(args[:min(2, len(args))], " "), t)
		}
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()), nil
}

// AuthToken runs `auth token`.
func (c *Client) AuthToken(ctx context.Context) (string, error) {
	tok, err := c.run(ctx, "auth", "token")
	if err == nil && tok == "" {
		return "", errors.New("adapter returned an empty token")
	}
	return tok, err
}

// List runs `pr list --reviewer @me --state open`.
func (c *Client) List(ctx context.Context) ([]PR, error) {
	out, err := c.run(ctx, "pr", "list", "--reviewer", "@me", "--state", "open")
	if err != nil {
		return nil, err
	}
	var prs []PR
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("invalid JSON from adapter pr list: %w", err)
	}
	return prs, nil
}

// View runs `pr view URL`.
func (c *Client) View(ctx context.Context, url string) (PR, error) {
	out, err := c.run(ctx, "pr", "view", url)
	if err != nil {
		return PR{}, err
	}
	var pr PR
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return PR{}, fmt.Errorf("invalid JSON from adapter pr view %s: %w", url, err)
	}
	if pr.URL == "" {
		pr.URL = url
	}
	return pr, nil
}

// Approve runs `pr review URL --approve`.
func (c *Client) Approve(ctx context.Context, url string) error {
	_, err := c.run(ctx, "pr", "review", url, "--approve")
	return err
}

// Merge runs `pr merge URL`.
func (c *Client) Merge(ctx context.Context, url string) error {
	_, err := c.MergeWarn(ctx, url)
	return err
}

// MergeWarn is Merge that also returns the adapter's warnings: whatever a successful merge wrote to stderr
// (for example that the source branch could not be deleted), one trimmed line each. An adapter that prints
// nothing yields none.
func (c *Client) MergeWarn(ctx context.Context, url string) ([]string, error) {
	_, errText, err := c.runFor(ctx, MergeTimeout, "pr", "merge", url)
	if err != nil {
		return nil, err
	}
	var warns []string
	for _, l := range strings.Split(errText, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			warns = append(warns, l)
		}
	}
	return warns, nil
}

// Comment runs `pr comment URL --body TEXT`. The body is passed as a single argv entry (no shell).
func (c *Client) Comment(ctx context.Context, url, body string) error {
	_, err := c.run(ctx, "pr", "comment", url, "--body", body)
	return err
}

// Close runs `pr close URL [--comment TEXT]` (Bitbucket decline). An empty comment omits --comment.
// With a comment the adapter posts it first and aborts the decline when that fails.
func (c *Client) Close(ctx context.Context, url, comment string) error {
	args := []string{"pr", "close", url}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	_, err := c.run(ctx, args...)
	return err
}
