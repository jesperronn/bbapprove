package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

const maxBody = 64 << 20

// Client talks to one Bitbucket Server. Create it with NewClient; it is safe for concurrent use.
type Client struct {
	env   Env
	base  string
	token string
	hc    *http.Client
}

// NewClient validates the connection settings (BaseURL and Token are required; trailing slashes on BaseURL are
// dropped). It reads nothing else and makes no request.
func NewClient(env Env) (*Client, error) {
	base := strings.TrimRight(env.BaseURL, "/")
	if base == "" {
		return nil, usagef("bitbucket adapter: BBAPPROVE_BASE_URL is not set")
	}
	if env.Token == "" {
		return nil, usagef("bitbucket adapter: BBAPPROVE_TOKEN is not set")
	}
	connect, total := env.ConnectTimeout, env.MaxTime
	if connect <= 0 {
		connect = DefaultConnectTimeout
	}
	if total <= 0 {
		total = DefaultMaxTime
	}
	env.ConnectTimeout, env.MaxTime = connect, total
	var rt http.RoundTripper = env.Transport
	if rt == nil {
		rt = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: connect}).DialContext,
			TLSHandshakeTimeout: connect,
			DisableKeepAlives:   true, // one connection per request, like one curl per request
		}
	}
	hc := &http.Client{
		Transport: rt,
		Timeout:   total,
		// Never follow a redirect: it would drop the bearer token on a host change and hide a wrong base URL.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{env: env, base: base, token: env.Token, hc: hc}, nil
}

type response struct {
	code     int
	body     []byte
	location string
}

func (r response) ok() bool { return r.code >= 200 && r.code < 300 }

// fail is the error for an unexpected status. A 3xx is reported as a redirect because it nearly always means
// a wrong BBAPPROVE_BASE_URL; any other status uses fallback.
func (r response) fail(fallback string) error {
	if r.code >= 300 && r.code < 400 {
		loc := r.location
		if loc == "" {
			loc = "(no Location header)"
		}
		return failf("redirect to %s; check BBAPPROVE_BASE_URL / repository", loc)
	}
	return failf("%s", fallback)
}

// do performs one request. The error is a transport failure, already phrased for the user. The token goes
// only into the Authorization header and is never part of any message.
func (c *Client) do(ctx context.Context, method, endpoint string, data []byte) (response, error) {
	target := c.base + endpoint
	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return response{}, failf("%s %s: connection failed", method, endpointPath(endpoint))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	path := req.URL.Path
	resp, err := c.hc.Do(req)
	if err != nil {
		return response{}, c.transportError(ctx, method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return response{}, c.transportError(ctx, method, path, err)
	}
	return response{code: resp.StatusCode, body: b, location: resp.Header.Get("Location")}, nil
}

func (c *Client) transportError(ctx context.Context, method, path string, err error) error {
	switch {
	case ctx.Err() != nil:
		return failf("%s %s: canceled", method, path)
	case isTimeout(err):
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return failf("%s %s: timed out after %gs", method, path, c.env.ConnectTimeout.Seconds())
		}
		return failf("%s %s: timed out after %gs", method, path, c.env.MaxTime.Seconds())
	}
	return failf("%s %s: connection failed", method, path)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// endpointPath is endpoint without its query string.
func endpointPath(endpoint string) string {
	p, _, _ := strings.Cut(endpoint, "?")
	return p
}

// decode unmarshals a 200 reply. A body that is not JSON, or JSON of the wrong shape, is an error naming the
// endpoint.
func decode(endpoint string, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		if json.Valid(body) {
			return failf("unexpected response from %s: unexpected JSON shape", endpointPath(endpoint))
		}
		return failf("unexpected response from %s: not JSON", endpointPath(endpoint))
	}
	return nil
}

// whoami returns the logged-in user's slug, or "" when it cannot be told (a failure just means not approved).
func (c *Client) whoami(ctx context.Context) string {
	resp, err := c.do(ctx, "GET", "/plugins/servlet/applinks/whoami", nil)
	if err != nil || resp.code != 200 {
		return ""
	}
	return strings.Trim(string(resp.body), " \t\n\v\f\r")
}

// loadStats fetches build status for all shas in one request. A failed request means "no builds known"; a 200
// reply that is not JSON is an error.
func (c *Client) loadStats(ctx context.Context, shas []string) (map[string]buildCounts, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range shas {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, s)
	}
	b.WriteByte(']')
	const ep = "/rest/build-status/1.0/commits/stats"
	resp, err := c.do(ctx, "POST", ep, []byte(b.String()))
	if err != nil || resp.code != 200 {
		return nil, nil
	}
	var stats map[string]buildCounts
	if err := decode(ep, resp.body, &stats); err != nil {
		return nil, err
	}
	return stats, nil
}
