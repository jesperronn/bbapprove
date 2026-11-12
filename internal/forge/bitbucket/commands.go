package bitbucket

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
)

const (
	dashboardPath = "/rest/api/1.0/dashboard/pull-requests"
	maxItemPages  = 50 // activities and blocker comments: at most this many pages of 100
)

var prURLRe = regexp.MustCompile(`/projects/([^/]+)/repos/([^/]+)/pull-requests/([0-9]+)`)

// prRef identifies a pull request on the server; the host part of a pasted URL is ignored.
type prRef struct{ key, repo, id string }

func parsePRURL(u string) (prRef, error) {
	m := prURLRe.FindStringSubmatch(u)
	if m == nil {
		return prRef{}, usagef("Not a PR URL: %s", u)
	}
	return prRef{m[1], m[2], m[3]}, nil
}

func (r prRef) path() string {
	return "/rest/api/1.0/projects/" + r.key + "/repos/" + r.repo + "/pull-requests/" + r.id
}

// ListPRs returns the open pull requests where the logged-in user is a reviewer, with build status.
func (c *Client) ListPRs(ctx context.Context) ([]PR, error) {
	me := c.whoami(ctx)
	var raws []rawPR
	for start := 0; ; {
		ep := fmt.Sprintf("%s?role=REVIEWER&state=OPEN&limit=100&start=%d&withProperties=true&withAttributes=true", dashboardPath, start)
		resp, err := c.do(ctx, "GET", ep, nil)
		if err != nil {
			return nil, err
		}
		if resp.code != 200 {
			return nil, resp.fail(fmt.Sprintf("Dashboard fetch failed (%d)", resp.code))
		}
		var page struct {
			Values        []rawPR `json:"values"`
			IsLastPage    bool    `json:"isLastPage"`
			NextPageStart *int    `json:"nextPageStart"`
		}
		if err := decode(ep, resp.body, &page); err != nil {
			return nil, err
		}
		raws = append(raws, page.Values...)
		if page.IsLastPage {
			break
		}
		if page.NextPageStart == nil || *page.NextPageStart <= start {
			return nil, failf("unexpected response from %s: more pages but no valid nextPageStart", dashboardPath)
		}
		start = *page.NextPageStart
	}
	var shas []string
	for _, r := range raws {
		if r.FromRef.LatestCommit != nil {
			shas = append(shas, *r.FromRef.LatestCommit)
		}
	}
	stats, err := c.loadStats(ctx, shas)
	if err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(raws))
	for _, r := range raws {
		prs = append(prs, normalize(r, c.base, me, stats))
	}
	return prs, nil
}

// ViewPR returns one pull request. The single-PR endpoint carries no properties, so the comment and task counts
// come from the activity feed and the blocker comments (tasks).
//
// TODO: QualityGate is always "unknown" here because the single-PR GET has no qgStatus, while ListPRs reports the
// real value. Check against a real server whether another endpoint exposes it; kept as in the bash adapter.
func (c *Client) ViewPR(ctx context.Context, prURL string) (PR, error) {
	ref, err := parsePRURL(prURL)
	if err != nil {
		return PR{}, err
	}
	return c.view(ctx, ref, prURL)
}

func (c *Client) view(ctx context.Context, ref prRef, given string) (PR, error) {
	me := c.whoami(ctx)
	resp, err := c.do(ctx, "GET", ref.path(), nil)
	if err != nil {
		return PR{}, err
	}
	if resp.code != 200 {
		return PR{}, resp.fail(fmt.Sprintf("Fetch failed (%d): %s", resp.code, given))
	}
	var raw rawPR
	if err := decode(ref.path(), resp.body, &raw); err != nil {
		return PR{}, err
	}
	var shas []string
	if raw.FromRef.LatestCommit != nil {
		shas = []string{*raw.FromRef.LatestCommit}
	}
	stats, err := c.loadStats(ctx, shas)
	if err != nil {
		return PR{}, err
	}
	// A task is a blocker comment: report comments excluding tasks, as the dashboard's commentCount does.
	commented, _, _ := c.countItems(ctx, ref.path()+"/activities", "COMMENTED")
	_, tasks, open := c.countItems(ctx, ref.path()+"/blocker-comments", "")
	raw.Properties.CommentCount = max(commented-tasks, 0)
	raw.Properties.OpenTaskCount = open
	return normalize(raw, c.base, me, stats), nil
}

// countItems pages through endpoint (100 per page, at most maxItemPages pages) and counts the items whose
// action equals action (when set), all items, and the items in state OPEN. Any failed page makes the whole
// count 0, as the bash adapter treats a failed fetch; a page without a usable nextPageStart ends the paging.
func (c *Client) countItems(ctx context.Context, endpoint, action string) (matching, all, open int) {
	for start, pages := 0, 0; pages < maxItemPages; pages++ {
		ep := endpoint + "?limit=100"
		if start > 0 {
			ep += fmt.Sprintf("&start=%d", start)
		}
		resp, err := c.do(ctx, "GET", ep, nil)
		if err != nil || resp.code != 200 {
			return 0, 0, 0
		}
		var page struct {
			Values []struct {
				Action string `json:"action"`
				State  string `json:"state"`
			} `json:"values"`
			IsLastPage    bool `json:"isLastPage"`
			NextPageStart *int `json:"nextPageStart"`
		}
		if decode(ep, resp.body, &page) != nil {
			return 0, 0, 0
		}
		for _, v := range page.Values {
			all++
			if action != "" && v.Action == action {
				matching++
			}
			if v.State == "OPEN" {
				open++
			}
		}
		if page.IsLastPage || page.NextPageStart == nil || *page.NextPageStart <= start {
			break
		}
		start = *page.NextPageStart
	}
	return matching, all, open
}

// Approve approves the pull request as the logged-in user.
func (c *Client) Approve(ctx context.Context, prURL string) error {
	ref, err := parsePRURL(prURL)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, "POST", ref.path()+"/approve", nil)
	if err != nil {
		return err
	}
	if !resp.ok() {
		return resp.fail(fmt.Sprintf("approve HTTP %d", resp.code))
	}
	return nil
}

// Merge merges the pull request. It re-reads the PR version before every attempt and retries only when the
// server says the PR went stale (HTTP 409, see staleConflict); see BBAPPROVE_MERGE_RETRIES and
// BBAPPROVE_RETRY_SLEEP.
//
// After a successful merge the source branch is deleted unless BBAPPROVE_DELETE_SOURCE_BRANCH is false. A
// deletion that fails never fails the merge; use MergeWarn to read the warning.
func (c *Client) Merge(ctx context.Context, prURL string) error {
	_, err := c.MergeWarn(ctx, prURL)
	return err
}

// MergeWarn is Merge that also returns the warnings of a successful merge (a source branch that could not be
// deleted), each one line as the CLI prints it on stderr.
func (c *Client) MergeWarn(ctx context.Context, prURL string) ([]string, error) {
	del, err := c.env.deleteSourceBranch()
	if err != nil {
		return nil, err
	}
	ref, err := parsePRURL(prURL)
	if err != nil {
		return nil, err
	}
	pr, err := c.transition(ctx, ref, "merge", "merge")
	if err != nil || !del {
		return nil, err
	}
	return c.deleteBranch(ctx, pr), nil
}

// prRefs is what the merge needs to know about the PR's source and target.
type prRefs struct {
	fromID, fromProject, fromRepo, toProject, toRepo, toID string
}

func parseRefs(body []byte) prRefs {
	var v struct {
		FromRef struct {
			ID   string `json:"id"`
			Repo struct {
				Slug    string `json:"slug"`
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
			} `json:"repository"`
		} `json:"fromRef"`
		ToRef struct {
			ID   string `json:"id"`
			Repo struct {
				Slug    string `json:"slug"`
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
			} `json:"repository"`
		} `json:"toRef"`
	}
	_ = decode("", body, &v)
	return prRefs{v.FromRef.ID, v.FromRef.Repo.Project.Key, v.FromRef.Repo.Slug, v.ToRef.Repo.Project.Key, v.ToRef.Repo.Slug, v.ToRef.ID}
}

// deleteBranch removes the merged PR's source branch when it is a branch of the PR's own repository other than
// the target. A branch that is already gone (404) is fine; any other failure is returned as a warning.
func (c *Client) deleteBranch(ctx context.Context, r prRefs) []string {
	if !strings.HasPrefix(r.fromID, "refs/heads/") || r.fromID == r.toID || r.fromProject != r.toProject || r.fromRepo != r.toRepo || r.toRepo == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString(`{"name":`)
	writeJSONString(&b, r.fromID)
	b.WriteString(`,"dryRun":false}`)
	resp, err := c.do(ctx, "DELETE", fmt.Sprintf("/rest/branch-utils/1.0/projects/%s/repos/%s/branches", r.toProject, r.toRepo), []byte(b.String()))
	var warns []string
	code := resp.code
	if err != nil {
		warns = append(warns, err.Error())
		code = 0
	} else if resp.ok() || resp.code == 404 {
		return nil
	}
	return append(warns, fmt.Sprintf("merge ok, but could not delete branch %s (HTTP %03d)", strings.TrimPrefix(r.fromID, "refs/heads/"), code))
}

// Comment posts one comment.
func (c *Client) Comment(ctx context.Context, prURL, text string) error {
	ref, err := parsePRURL(prURL)
	if err != nil {
		return err
	}
	return c.comment(ctx, ref, text)
}

func (c *Client) comment(ctx context.Context, ref prRef, text string) error {
	var b strings.Builder
	// Laid out like jq's default (indented) output, which is what the bash adapter sends.
	b.WriteString("{\n  \"text\": ")
	writeJSONString(&b, text)
	b.WriteString("\n}")
	resp, err := c.do(ctx, "POST", ref.path()+"/comments", []byte(b.String()))
	if err != nil {
		return err
	}
	if !resp.ok() {
		return resp.fail(fmt.Sprintf("comment HTTP %d", resp.code))
	}
	return nil
}

// Close declines the pull request. A non-empty comment is posted first; when that fails the PR is not declined.
// A comment stays posted when the decline later fails.
func (c *Client) Close(ctx context.Context, prURL, comment string) error {
	ref, err := parsePRURL(prURL)
	if err != nil {
		return err
	}
	if comment != "" {
		if err := c.comment(ctx, ref, comment); err != nil {
			return &Error{Msg: err.Error() + "\nclose aborted: comment failed, PR not declined", Exit: 1}
		}
	}
	_, err = c.transition(ctx, ref, "close", "decline")
	return err
}

// transition merges or declines (action) with the same stale-version retry loop. name prefixes the version-read
// error.
func (c *Client) transition(ctx context.Context, ref prRef, name, action string) (prRefs, error) {
	attempts, sleep, err := c.env.retryPolicy()
	if err != nil {
		return prRefs{}, err
	}
	for attempt := 1; ; attempt++ {
		version, refs, err := c.readVersion(ctx, ref, name)
		if err != nil {
			return prRefs{}, err
		}
		ep := fmt.Sprintf("%s/%s?version=%d", ref.path(), action, version)
		resp, err := c.do(ctx, "POST", ep, []byte(fmt.Sprintf(`{"version":%d}`, version)))
		if err != nil {
			return prRefs{}, err
		}
		if resp.ok() {
			return refs, nil
		}
		if staleConflict(resp.code, resp.body) && attempt < attempts {
			c.env.sleep(ctx, sleep)
			continue
		}
		return prRefs{}, resp.fail(fmt.Sprintf("%s HTTP %d: %s", action, resp.code, errorMessages(resp.body)))
	}
}

// staleConflict reports whether a merge or decline answer means the PR must be re-read and the call retried: an
// out-of-date version, or the target branch moving while the merge was being prepared ("New changes were pushed
// to <branch> in <repo>"), which Bitbucket raises until the PR is reloaded.
func staleConflict(code int, body []byte) bool {
	return code == 409 && (bytes.Contains(body, []byte("out-of-date")) || bytes.Contains(body, []byte("New changes were pushed")))
}

// readVersion fetches the PR's current version; a version read earlier goes stale when the target branch moves.
func (c *Client) readVersion(ctx context.Context, ref prRef, name string) (int64, prRefs, error) {
	resp, err := c.do(ctx, "GET", ref.path(), nil)
	if err != nil {
		return 0, prRefs{}, err
	}
	if resp.code != 200 {
		return 0, prRefs{}, resp.fail(fmt.Sprintf("%s: could not read PR version (HTTP %d)", name, resp.code))
	}
	var v struct {
		Version *int64 `json:"version"`
	}
	if err := decode(ref.path(), resp.body, &v); err != nil {
		return 0, prRefs{}, err
	}
	if v.Version == nil {
		return 0, prRefs{}, failf("%s: could not read PR version (HTTP %d)", name, resp.code)
	}
	return *v.Version, parseRefs(resp.body), nil
}

// errorMessages joins the messages of Bitbucket's {"errors":[{"message":...}]} body with "; ", or "" when the
// body has none or is not JSON.
func errorMessages(body []byte) string {
	var e struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if decode("", body, &e) != nil {
		return ""
	}
	msgs := make([]string, 0, len(e.Errors))
	for _, m := range e.Errors {
		msgs = append(msgs, m.Message)
	}
	return strings.Join(msgs, "; ")
}
