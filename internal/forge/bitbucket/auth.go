package bitbucket

import (
	"context"
	"io"
	"strings"
)

const noTokenMsg = "No token: set BBAPPROVE_TOKEN, or BBAPPROVE_TOKEN_NAME to a 1Password item with a token or credential field."

// AuthToken returns the API token: BBAPPROVE_TOKEN when set, else the 1Password item named by
// BBAPPROVE_TOKEN_NAME (field `token`, then `credential`) read with the `op` CLI. A dim notice goes to stderr
// before op runs, because op may wait for the user to unlock 1Password. It needs no base URL.
func AuthToken(ctx context.Context, env Env, stderr io.Writer) (string, error) {
	token := env.Token
	if token == "" && env.TokenName != "" {
		op, err := env.lookPath("op")
		if err != nil {
			return "", failf("1Password CLI (op) not found; set BBAPPROVE_TOKEN instead")
		}
		dim, reset := "", ""
		if env.NoColor == "" && (env.StderrIsTTY || env.ForceColor != "") {
			dim, reset = "\x1b[2m", "\x1b[0m"
		}
		io.WriteString(stderr, dim+"Waiting for 1Password: op item get "+env.TokenName+reset+"\n")
		for _, field := range []string{"token", "credential"} {
			out, err := env.runOp(ctx, op, []string{"item", "get", env.TokenName, "--reveal", "--fields", "label=" + field})
			if err != nil {
				out = ""
			}
			if token = strings.TrimRight(out, "\n"); token != "" {
				break
			}
		}
	}
	if token == "" {
		return "", failf("%s", noTokenMsg)
	}
	return token, nil
}
