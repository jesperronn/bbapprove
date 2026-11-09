package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const usageMsg = "usage: bitbucket auth token | pr list|view|review|merge|comment|close ..."

// Run executes one adapter command line (args without the program name) and returns the exit code: 0 success,
// 1 failure, 2 usage or configuration error. Output goes to stdout, one-line errors to stderr. The token is
// printed only by `auth token`.
func Run(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	err := run(ctx, args, env, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
	}
	return ExitCode(err)
}

func run(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) error {
	var group, sub string
	if len(args) > 0 {
		group = args[0]
	}
	if len(args) > 1 {
		sub = args[1]
	}
	var rest []string
	if len(args) > 2 {
		rest = args[2:]
	}
	if group == "auth" && sub == "token" {
		if len(rest) > 0 {
			return usagef("auth token: unknown argument: %s", rest[0])
		}
		tok, err := AuthToken(ctx, env, stderr)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, tok)
		return nil
	}
	c, err := NewClient(env)
	if err != nil {
		return err
	}
	if group != "pr" {
		return usagef("%s", usageMsg)
	}
	switch sub {
	case "list":
		return cmdList(ctx, c, rest, stdout)
	case "view":
		return cmdView(ctx, c, rest, stdout)
	case "review":
		return cmdReview(ctx, c, rest)
	case "merge":
		return cmdMerge(ctx, c, rest, stderr)
	case "comment":
		return cmdComment(ctx, c, rest, env.Stdin)
	case "close":
		return cmdClose(ctx, c, rest)
	}
	return usagef("%s", usageMsg)
}

func cmdList(ctx context.Context, c *Client, args []string, stdout io.Writer) error {
	var reviewer, state int
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return usagef("pr list: only --reviewer @me --state open is supported")
		}
		switch {
		case args[i] == "--reviewer" && args[i+1] == "@me":
			reviewer++
		case args[i] == "--state" && args[i+1] == "open":
			state++
		default:
			return usagef("pr list: only --reviewer @me --state open is supported")
		}
	}
	if reviewer != 1 || state != 1 {
		return usagef("pr list: only --reviewer @me --state open is supported")
	}
	prs, err := c.ListPRs(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n", MarshalPRs(prs))
	return nil
}

// prArg splits "URL [extra...]" of a pr subcommand: a missing URL is a usage error, and the URL must parse.
func prArg(sub string, args []string) (url string, rest []string, err error) {
	if len(args) == 0 {
		return "", nil, usagef("pr %s: missing PR URL", sub)
	}
	if _, err := parsePRURL(args[0]); err != nil {
		return "", nil, err
	}
	return args[0], args[1:], nil
}

func noExtra(sub string, rest []string) error {
	if len(rest) > 0 {
		return usagef("pr %s: unknown argument: %s", sub, rest[0])
	}
	return nil
}

func cmdView(ctx context.Context, c *Client, args []string, stdout io.Writer) error {
	url, rest, err := prArg("view", args)
	if err != nil {
		return err
	}
	if err := noExtra("view", rest); err != nil {
		return err
	}
	p, err := c.ViewPR(ctx, url)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n", MarshalPR(p))
	return nil
}

func cmdReview(ctx context.Context, c *Client, args []string) error {
	if len(args) == 0 {
		return usagef("pr review: missing PR URL")
	}
	// The flag is checked before the URL, as the bash adapter does.
	if len(args) < 2 || args[1] != "--approve" {
		return usagef("pr review: only --approve is supported")
	}
	if _, _, err := prArg("review", args); err != nil {
		return err
	}
	if err := noExtra("review", args[2:]); err != nil {
		return err
	}
	return c.Approve(ctx, args[0])
}

func cmdMerge(ctx context.Context, c *Client, args []string, stderr io.Writer) error {
	url, rest, err := prArg("merge", args)
	if err != nil {
		return err
	}
	if err := noExtra("merge", rest); err != nil {
		return err
	}
	warns, err := c.MergeWarn(ctx, url)
	for _, w := range warns {
		fmt.Fprintln(stderr, w)
	}
	return err
}

// blank reports whether s is empty or only ASCII whitespace.
func blank(s string) bool { return strings.Trim(s, " \t\n\v\f\r") == "" }

func cmdComment(ctx context.Context, c *Client, args []string, stdin io.Reader) error {
	url, rest, err := prArg("comment", args)
	if err != nil {
		return err
	}
	var body, file string
	haveBody := false
	for len(rest) > 0 {
		switch rest[0] {
		case "--body":
			if len(rest) < 2 {
				return usagef("pr comment: --body needs a value")
			}
			body, haveBody, rest = rest[1], true, rest[2:]
		case "--body-file":
			if len(rest) < 2 {
				return usagef("pr comment: --body-file needs a value")
			}
			file, rest = rest[1], rest[2:]
		default:
			return usagef("pr comment: unknown argument: %s", rest[0])
		}
	}
	if file != "" {
		if haveBody {
			return usagef("pr comment: use either --body or --body-file, not both")
		}
		body, err = readBodyFile(file, stdin)
		if err != nil {
			return err
		}
	}
	if blank(body) {
		return usagef("pr comment: empty comment (use --body TEXT or --body-file FILE)")
	}
	return c.Comment(ctx, url, body)
}

// readBodyFile reads a comment from a file, or from stdin for "-". Trailing newlines are dropped.
func readBodyFile(name string, stdin io.Reader) (string, error) {
	var b []byte
	var err error
	if name == "-" {
		if stdin != nil {
			b, err = io.ReadAll(stdin)
		}
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		reason := err.Error()
		var pe *os.PathError
		if errors.As(err, &pe) {
			reason = pe.Err.Error()
		}
		return "", usagef("pr comment: cannot read %s: %s", name, reason)
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func cmdClose(ctx context.Context, c *Client, args []string) error {
	url, rest, err := prArg("close", args)
	if err != nil {
		return err
	}
	var comment string
	haveComment := false
	for len(rest) > 0 {
		if rest[0] != "--comment" {
			return usagef("pr close: unknown argument: %s", rest[0])
		}
		if len(rest) < 2 {
			return usagef("pr close: --comment needs a value")
		}
		comment, haveComment, rest = rest[1], true, rest[2:]
	}
	if haveComment && blank(comment) {
		return usagef("pr close: --comment must not be empty")
	}
	return c.Close(ctx, url, comment)
}
