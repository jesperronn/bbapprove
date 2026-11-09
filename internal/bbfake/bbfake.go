// Package bbfake is a fake Bitbucket Server REST API for tests. It is test support only: nothing in the
// shipped binary imports it.
//
// It implements exactly the endpoints adapters/bitbucket uses (whoami, the reviewer dashboard, pull request
// view, activities, blocker comments, batched build-status stats, approve, merge, decline, comments, branch
// deletion through branch-utils), over
// net/http/httptest, with code-configurable scenario data. Every request is recorded (method, path, query,
// body, headers) so tests can assert on what the adapter sent, and failures can be injected per endpoint
// (HTTP errors, dropped connections, slow answers). It never touches the network beyond loopback.
package bbfake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OutOfDateMessage is what Bitbucket answers (HTTP 409) when the PR version sent is stale.
const OutOfDateMessage = "You are attempting to modify a pull request based on out-of-date information."

// Person is a reviewer or participant of a PR.
type Person struct {
	Slug   string
	Email  string
	Status string // APPROVED, NEEDS_WORK, UNAPPROVED
}

// PR is one pull request of the scenario. Dashboard and view responses are derived from it the way
// Bitbucket derives them: the dashboard carries "properties", a single-PR GET does not.
type PR struct {
	Project, Repo string
	ID            int
	Version       int
	Title         string
	AuthorEmail   string
	AuthorSlug    string
	Branch        string // fromRef.displayId
	Commit        string // fromRef.latestCommit; empty omits the field
	Reviewers     []Person
	Participants  []Person
	State         string // OPEN (default), MERGED, DECLINED

	// Source and target refs. FromRefID overrides the source ref id (default refs/heads/<Branch>), e.g. a tag;
	// Target is the target branch name (default main). ForkProject/ForkRepo, when set, make the source branch
	// live in that other repository (a fork PR). BranchGone makes the branch-utils DELETE answer 404.
	FromRefID, Target     string
	ForkProject, ForkRepo string
	BranchGone            bool

	// Dashboard properties. QGStatus "" omits qgStatus.
	QGStatus     string
	CommentCount int
	OpenTasks    int

	// Activity feed and blocker comments (tasks) served to `pr view`.
	CommentedActivities int
	OtherActivities     int
	Tasks               []string // state of each blocker comment: OPEN or RESOLVED

	// StaleMerges makes the next N merge calls answer 409 out-of-date and bump Version (a concurrent merge
	// moved the target branch). StaleDeclines is the same for decline.
	StaleMerges, StaleDeclines int
	// MergeVetoes, when set, makes merge answer 409 with these messages (checks failed, needs approval, ...).
	MergeVetoes []string
}

// BuildStats is the per-commit entry of the batched build-status response.
type BuildStats struct {
	Successful int `json:"successful"`
	Failed     int `json:"failed"`
	InProgress int `json:"inProgress"`
}

// Fault is one injected failure. Delay applies first; then, unless Drop, Status and Body are written (when
// Status is 0 the request is served normally after the delay). Drop closes the connection with no response.
type Fault struct {
	Status int
	Body   string
	Delay  time.Duration
	Drop   bool
	// Times is how many requests the fault applies to: 0 means once, a negative number means always.
	Times int
}

// Scenario is the data the fake serves. Zero value is an empty server (no PRs, nobody logged in).
type Scenario struct {
	Token       string // when set, requests without "Authorization: Bearer <Token>" get 401
	Me          string // whoami slug; empty answers an empty 200 body
	ContextPath string // optional URL prefix such as "/bitbucket"
	PageSize    int    // dashboard page size; 0 serves everything on one page
	PRs         []*PR
	Builds      map[string]BuildStats // commit sha -> stats; commits not listed are absent from the response
}

// Request is one recorded request as the server saw it.
type Request struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Query         string `json:"query,omitempty"`
	Body          string `json:"body,omitempty"`
	Authorization string `json:"authorization,omitempty"`
	Accept        string `json:"accept,omitempty"`
	ContentType   string `json:"contentType,omitempty"`
}

type faultEntry struct {
	method, suffix string
	f              Fault
	left           int
}

// Server is a running fake. Create with New, stop with Close.
type Server struct {
	hs *httptest.Server

	mu       sync.Mutex
	sc       *Scenario
	reqs     []Request
	faults   []*faultEntry
	comments map[int][]string // PR id -> comments posted
	deleted  []string         // "PROJECT/repo:ref" of every branch the DELETE endpoint removed
	nextCmt  int
}

// New starts a fake serving sc (nil means an empty scenario).
func New(sc *Scenario) *Server {
	if sc == nil {
		sc = &Scenario{}
	}
	s := &Server{sc: sc, comments: map[int][]string{}}
	s.hs = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the server.
func (s *Server) Close() { s.hs.Close() }

// URL is the base URL to give the client (includes the scenario's ContextPath).
func (s *Server) URL() string { return s.hs.URL + s.sc.ContextPath }

// Requests returns a copy of every recorded request, in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Comments returns the comment texts posted to PR id.
func (s *Server) Comments(id int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.comments[id]...)
}

// Fail injects faults for requests matching pattern, "METHOD /path/suffix" (METHOD may be "*"). The suffix is
// matched against the end of the path below ContextPath, e.g. "POST /merge", "GET /dashboard/pull-requests".
// Faults are consumed in the order added.
func (s *Server) Fail(pattern string, faults ...Fault) {
	method, suffix, _ := strings.Cut(pattern, " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range faults {
		left := f.Times
		if left == 0 {
			left = 1
		}
		s.faults = append(s.faults, &faultEntry{method: method, suffix: suffix, f: f, left: left})
	}
}

// PR returns the scenario PR project/repo#id, or nil.
func (s *Server) PR(project, repo string, id int) *PR {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(project, repo, id)
}

func (s *Server) find(project, repo string, id int) *PR {
	for _, p := range s.sc.PRs {
		if p.Project == project && p.Repo == repo && p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Server) takeFault(method, path string) (Fault, bool) {
	for _, e := range s.faults {
		if e.left == 0 || (e.method != "*" && e.method != method) || !strings.HasSuffix(path, e.suffix) {
			continue
		}
		if e.left > 0 {
			e.left--
		}
		return e.f, true
	}
	return Fault{}, false
}

var (
	prPath      = regexp.MustCompile(`^/rest/api/1\.0/projects/([^/]+)/repos/([^/]+)/pull-requests/([0-9]+)(/[a-z-]+)?$`)
	dashboard   = "/rest/api/1.0/dashboard/pull-requests"
	whoamiPath  = "/plugins/servlet/applinks/whoami"
	buildsStats = "/rest/build-status/1.0/commits/stats"
	branchPath  = regexp.MustCompile(`^/rest/branch-utils/1\.0/projects/([^/]+)/repos/([^/]+)/branches$`)
)

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body),
		Authorization: r.Header.Get("Authorization"), Accept: r.Header.Get("Accept"), ContentType: r.Header.Get("Content-Type"),
	})
	path, ok := strings.CutPrefix(r.URL.Path, s.sc.ContextPath)
	if !ok {
		path = "\x00" + r.URL.Path // outside the context path: matches nothing
	}
	fault, hasFault := s.takeFault(r.Method, path)
	s.mu.Unlock()

	if hasFault {
		if fault.Delay > 0 {
			select {
			case <-time.After(fault.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if fault.Drop {
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
				}
			}
			return
		}
		if fault.Status != 0 {
			writeRaw(w, fault.Status, fault.Body)
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sc.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.sc.Token {
		writeErrors(w, 401, "Authentication failed")
		return
	}
	switch {
	case r.Method == "GET" && path == whoamiPath:
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, s.sc.Me)
	case r.Method == "GET" && path == dashboard:
		s.serveDashboard(w, r)
	case r.Method == "POST" && path == buildsStats:
		s.serveStats(w, body)
	case r.Method == "DELETE" && branchPath.MatchString(path):
		s.deleteBranch(w, branchPath.FindStringSubmatch(path), body)
	default:
		m := prPath.FindStringSubmatch(path)
		if m == nil {
			writeErrors(w, 404, "No such resource: "+path)
			return
		}
		id, _ := strconv.Atoi(m[3])
		pr := s.find(m[1], m[2], id)
		if pr == nil {
			writeErrors(w, 404, fmt.Sprintf("Pull request %d does not exist in %s/%s.", id, m[1], m[2]))
			return
		}
		s.servePR(w, r, pr, m[4], body)
	}
}

func (s *Server) servePR(w http.ResponseWriter, r *http.Request, pr *PR, sub string, body []byte) {
	switch {
	case r.Method == "GET" && sub == "":
		writeJSON(w, 200, prJSON(s.sc, pr, false))
	case r.Method == "GET" && sub == "/activities":
		var vals []map[string]any
		for i := 0; i < pr.CommentedActivities; i++ {
			vals = append(vals, map[string]any{"id": i + 1, "action": "COMMENTED"})
		}
		for i := 0; i < pr.OtherActivities; i++ {
			vals = append(vals, map[string]any{"id": 1000 + i, "action": "OPENED"})
		}
		writeJSON(w, 200, map[string]any{"size": len(vals), "isLastPage": true, "values": nonNil(vals)})
	case r.Method == "GET" && sub == "/blocker-comments":
		var vals []map[string]any
		for i, st := range pr.Tasks {
			vals = append(vals, map[string]any{"id": 5000 + i, "severity": "BLOCKER", "state": st})
		}
		writeJSON(w, 200, map[string]any{"size": len(vals), "isLastPage": true, "values": nonNil(vals)})
	case r.Method == "POST" && sub == "/approve":
		s.approve(pr)
		writeJSON(w, 200, map[string]any{"role": "REVIEWER", "approved": true, "status": "APPROVED", "user": map[string]any{"slug": s.sc.Me}})
	case r.Method == "POST" && sub == "/comments":
		var in struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeErrors(w, 400, "Malformed JSON body")
			return
		}
		s.nextCmt++
		s.comments[pr.ID] = append(s.comments[pr.ID], in.Text)
		pr.CommentedActivities++
		writeJSON(w, 201, map[string]any{"id": s.nextCmt, "text": in.Text})
	case r.Method == "POST" && (sub == "/merge" || sub == "/decline"):
		s.transition(w, r, pr, strings.TrimPrefix(sub, "/"))
	default:
		writeErrors(w, 404, "No such resource: "+r.Method+" "+r.URL.Path)
	}
}

// deleteBranch implements DELETE branch-utils/.../branches: body {"name": ref, "dryRun": bool}. It answers 204
// when the branch existed and 404 when it is gone (a PR with BranchGone, or a branch already deleted).
func (s *Server) deleteBranch(w http.ResponseWriter, m []string, body []byte) {
	var in struct {
		Name   string `json:"name"`
		DryRun bool   `json:"dryRun"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Name == "" {
		writeErrors(w, 400, "Malformed JSON body")
		return
	}
	key := m[1] + "/" + m[2] + ":" + in.Name
	gone := false
	for _, d := range s.deleted {
		gone = gone || d == key
	}
	for _, p := range s.sc.PRs {
		pp, pr := p.sourceRepo()
		if pp == m[1] && pr == m[2] && p.fromRefID() == in.Name && p.BranchGone {
			gone = true
		}
	}
	if gone {
		writeErrors(w, 404, "Branch "+in.Name+" does not exist in "+m[1]+"/"+m[2]+".")
		return
	}
	if !in.DryRun {
		s.deleted = append(s.deleted, key)
	}
	w.WriteHeader(204)
}

func (p *PR) sourceRepo() (project, repo string) {
	if p.ForkRepo != "" {
		return p.ForkProject, p.ForkRepo
	}
	return p.Project, p.Repo
}

func (p *PR) fromRefID() string { return nonEmpty(p.FromRefID, "refs/heads/"+p.Branch) }

func (s *Server) approve(pr *PR) {
	for i := range pr.Reviewers {
		if pr.Reviewers[i].Slug == s.sc.Me {
			pr.Reviewers[i].Status = "APPROVED"
			return
		}
	}
	pr.Reviewers = append(pr.Reviewers, Person{Slug: s.sc.Me, Status: "APPROVED"})
}

// transition implements merge and decline: the version in the query must match the PR's current version.
func (s *Server) transition(w http.ResponseWriter, r *http.Request, pr *PR, verb string) {
	stale := &pr.StaleMerges
	if verb == "decline" {
		stale = &pr.StaleDeclines
	}
	if *stale > 0 {
		*stale--
		pr.Version++
		writeErrors(w, 409, OutOfDateMessage)
		return
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("version")); err != nil || v != pr.Version {
		writeErrors(w, 409, OutOfDateMessage)
		return
	}
	if verb == "merge" && len(pr.MergeVetoes) > 0 {
		writeErrors(w, 409, pr.MergeVetoes...)
		return
	}
	pr.State = map[string]string{"merge": "MERGED", "decline": "DECLINED"}[verb]
	pr.Version++
	writeJSON(w, 200, prJSON(s.sc, pr, false))
}

func (s *Server) serveDashboard(w http.ResponseWriter, r *http.Request) {
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if s.sc.PageSize > 0 && (limit == 0 || s.sc.PageSize < limit) {
		limit = s.sc.PageSize
	}
	var open []*PR
	for _, p := range s.sc.PRs {
		if p.State == "" || p.State == "OPEN" {
			open = append(open, p)
		}
	}
	if start > len(open) {
		start = len(open)
	}
	end := len(open)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	vals := []any{}
	for _, p := range open[start:end] {
		vals = append(vals, prJSON(s.sc, p, true))
	}
	page := map[string]any{"size": len(vals), "limit": limit, "isLastPage": end == len(open), "values": vals, "start": start}
	if end < len(open) {
		page["nextPageStart"] = end
	}
	writeJSON(w, 200, page)
}

func (s *Server) serveStats(w http.ResponseWriter, body []byte) {
	var shas []string
	if err := json.Unmarshal(body, &shas); err != nil {
		writeErrors(w, 400, "Malformed JSON body")
		return
	}
	out := map[string]BuildStats{}
	for _, sha := range shas {
		if st, ok := s.sc.Builds[sha]; ok {
			out[sha] = st
		}
	}
	writeJSON(w, 200, out)
}

func people(ps []Person, role string) []any {
	out := []any{}
	for _, p := range ps {
		u := map[string]any{"slug": p.Slug, "name": p.Slug}
		if p.Email != "" {
			u["emailAddress"] = p.Email
		}
		out = append(out, map[string]any{"user": u, "role": role, "approved": p.Status == "APPROVED", "status": nonEmpty(p.Status, "UNAPPROVED")})
	}
	return out
}

// prJSON renders the raw Bitbucket PR object. withProps adds the dashboard's "properties".
func prJSON(sc *Scenario, p *PR, withProps bool) map[string]any {
	from := map[string]any{"id": p.fromRefID(), "displayId": p.Branch}
	fp, fr := p.sourceRepo()
	from["repository"] = map[string]any{"slug": fr, "project": map[string]any{"key": fp}}
	if p.Commit != "" {
		from["latestCommit"] = p.Commit
	}
	repo := map[string]any{"slug": p.Repo, "project": map[string]any{"key": p.Project}}
	pr := map[string]any{
		"id": p.ID, "version": p.Version, "title": p.Title, "state": nonEmpty(p.State, "OPEN"),
		"author":       map[string]any{"user": map[string]any{"slug": p.AuthorSlug, "emailAddress": p.AuthorEmail}, "role": "AUTHOR"},
		"fromRef":      from,
		"toRef":        map[string]any{"id": "refs/heads/" + nonEmpty(p.Target, "main"), "displayId": nonEmpty(p.Target, "main"), "repository": repo},
		"reviewers":    people(p.Reviewers, "REVIEWER"),
		"participants": people(p.Participants, "PARTICIPANT"),
	}
	if withProps {
		props := map[string]any{"commentCount": p.CommentCount, "openTaskCount": p.OpenTasks}
		if p.QGStatus != "" {
			props["qgStatus"] = p.QGStatus
		}
		pr["properties"] = props
	}
	return pr
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	writeRaw(w, status, string(b))
}

func writeErrors(w http.ResponseWriter, status int, msgs ...string) {
	errs := []map[string]any{}
	for _, m := range msgs {
		errs = append(errs, map[string]any{"context": nil, "message": m})
	}
	writeJSON(w, status, map[string]any{"errors": errs})
}
