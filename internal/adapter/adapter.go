// Package adapter wraps the gh-style forge adapter CLI. It is the only way the Go code talks to the forge.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// Client runs the adapter executable as a subprocess.
type Client struct {
	Path    string
	Token   string // passed to the child as BBAPPROVE_TOKEN when set
	Environ []string
	Timeout time.Duration
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
	return c.runFor(ctx, DefaultTimeout, args...)
}

// runFor is run with an explicit per-call timeout (overridden by c.Timeout when set).
func (c *Client) runFor(ctx context.Context, def time.Duration, args ...string) (string, error) {
	t := c.timeout(def)
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Env = append(append([]string{}, c.Environ...), "BBAPPROVE_TOKEN="+c.Token)
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if ctx.Err() != nil {
			return "", fmt.Errorf("adapter %s timed out after %s", strings.Join(args[:min(2, len(args))], " "), t)
		}
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
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
	_, err := c.runFor(ctx, MergeTimeout, "pr", "merge", url)
	return err
}
