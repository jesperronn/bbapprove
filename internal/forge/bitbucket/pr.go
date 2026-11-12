package bitbucket

import (
	"strconv"
	"strings"
)

// PR is the normalized pull request, the adapter's output contract. The field order is the JSON key order.
type PR struct {
	URL          string
	Number       int
	Project      string
	Repo         string
	Title        string
	AuthorEmail  string
	AuthorLogin  string
	HeadBranch   string
	ApprovedByMe bool
	Build        string // passed|failed|running|none
	QualityGate  string // passed|failed|unknown
	Approvals    int
	NeedsWork    int
	CommentCount int
	OpenTasks    int
	State        string // OPEN|MERGED|DECLINED
}

// MarshalPR renders p as compact JSON with the exact key order and string escaping of the bash adapter (jq -c).
func MarshalPR(p PR) []byte {
	var b strings.Builder
	writePR(&b, p)
	return []byte(b.String())
}

// MarshalPRs renders a JSON array of PRs; no PRs give [].
func MarshalPRs(prs []PR) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range prs {
		if i > 0 {
			b.WriteByte(',')
		}
		writePR(&b, p)
	}
	b.WriteByte(']')
	return []byte(b.String())
}

func writePR(b *strings.Builder, p PR) {
	str := func(key, val string) {
		b.WriteString(`"` + key + `":`)
		writeJSONString(b, val)
	}
	num := func(key string, n int) { b.WriteString(`"` + key + `":` + strconv.Itoa(n)) }
	b.WriteByte('{')
	str("url", p.URL)
	b.WriteByte(',')
	num("number", p.Number)
	b.WriteByte(',')
	str("project", p.Project)
	b.WriteByte(',')
	str("repo", p.Repo)
	b.WriteByte(',')
	str("title", p.Title)
	b.WriteString(`,"author":{`)
	str("email", p.AuthorEmail)
	b.WriteByte(',')
	str("login", p.AuthorLogin)
	b.WriteString(`},`)
	str("headBranch", p.HeadBranch)
	b.WriteString(`,"approvedByMe":` + strconv.FormatBool(p.ApprovedByMe) + `,`)
	str("build", p.Build)
	b.WriteByte(',')
	str("qualityGate", p.QualityGate)
	b.WriteByte(',')
	num("approvals", p.Approvals)
	b.WriteByte(',')
	num("needsWork", p.NeedsWork)
	b.WriteByte(',')
	num("commentCount", p.CommentCount)
	b.WriteByte(',')
	num("openTasks", p.OpenTasks)
	b.WriteByte(',')
	str("state", p.State)
	b.WriteByte('}')
}

// writeJSONString writes s as a JSON string escaped the way jq does: short escapes for the usual control
// characters, \u00XX for the rest (and DEL), and everything else, including <, > and &, as is.
func writeJSONString(b *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u00` + string(hex[r>>4]) + string(hex[r&15]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// ---- the raw Bitbucket objects, only the fields we read ----

type rawPerson struct {
	User struct {
		Slug string `json:"slug"`
	} `json:"user"`
	Status string `json:"status"`
}

type rawPR struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Author struct {
		User struct {
			EmailAddress string `json:"emailAddress"`
			Slug         string `json:"slug"`
		} `json:"user"`
	} `json:"author"`
	FromRef struct {
		DisplayID    string  `json:"displayId"`
		LatestCommit *string `json:"latestCommit"`
	} `json:"fromRef"`
	ToRef struct {
		Repository struct {
			Slug    string `json:"slug"`
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"repository"`
	} `json:"toRef"`
	Reviewers    []rawPerson `json:"reviewers"`
	Participants []rawPerson `json:"participants"`
	Properties   struct {
		QGStatus      string `json:"qgStatus"`
		CommentCount  int    `json:"commentCount"`
		OpenTaskCount int    `json:"openTaskCount"`
	} `json:"properties"`
}

// buildCounts is one commit's entry of the batched build-status response.
type buildCounts struct {
	Successful float64 `json:"successful"`
	Failed     float64 `json:"failed"`
	InProgress float64 `json:"inProgress"`
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// normalize turns a raw PR into the output contract. me is the logged-in user's slug ("" when unknown).
func normalize(raw rawPR, base, me string, stats map[string]buildCounts) PR {
	commit := ""
	if raw.FromRef.LatestCommit != nil {
		commit = *raw.FromRef.LatestCommit
	}
	b := stats[commit]
	build := "none"
	switch {
	case b.Failed > 0:
		build = "failed"
	case b.InProgress > 0:
		build = "running"
	case b.Successful > 0:
		build = "passed"
	}
	gate := asciiLower(raw.Properties.QGStatus)
	if gate != "passed" && gate != "failed" {
		gate = "unknown"
	}
	p := PR{
		URL:          base + "/projects/" + raw.ToRef.Repository.Project.Key + "/repos/" + raw.ToRef.Repository.Slug + "/pull-requests/" + strconv.Itoa(raw.ID),
		Number:       raw.ID,
		Project:      raw.ToRef.Repository.Project.Key,
		Repo:         raw.ToRef.Repository.Slug,
		Title:        raw.Title,
		AuthorEmail:  raw.Author.User.EmailAddress,
		AuthorLogin:  raw.Author.User.Slug,
		HeadBranch:   raw.FromRef.DisplayID,
		Build:        build,
		QualityGate:  gate,
		CommentCount: raw.Properties.CommentCount,
		OpenTasks:    raw.Properties.OpenTaskCount,
		State:        nonEmptyState(raw.State),
	}
	for _, r := range raw.Reviewers {
		switch r.Status {
		case "APPROVED":
			p.Approvals++
		case "NEEDS_WORK":
			p.NeedsWork++
		}
	}
	if me != "" {
		for _, list := range [][]rawPerson{raw.Reviewers, raw.Participants} {
			for _, r := range list {
				if r.User.Slug == me && r.Status == "APPROVED" {
					p.ApprovedByMe = true
				}
			}
		}
	}
	return p
}

// nonEmptyState is the PR state, OPEN when Bitbucket sent none (as the bash adapter's `.state // "OPEN"`).
func nonEmptyState(s string) string {
	if s == "" {
		return "OPEN"
	}
	return s
}
