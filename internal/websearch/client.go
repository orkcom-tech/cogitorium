package websearch

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	// fetchTimeout is deliberately shorter than the server's shutdown grace,
	// so a fetch can never outlive Shutdown and strand an awaiting audit row.
	fetchTimeout = 8 * time.Second

	// readLimit guards the JSON PARSER and nothing else. It is not the budget
	// the model sees — that is maxResults and the per-field clips below,
	// applied after parsing, and they are what keep web_search from being
	// web_fetch with an extra hop.
	//
	// Set at 3 KB originally, which was a mistake worth naming: real answers
	// exceed it, so the body arrived truncated mid-object and every rich
	// result failed to parse. A cap that silently turns good responses into
	// "unexpected data" is worse than no cap.
	readLimit  = 256 << 10
	maxResults = 5

	maxTitle   = 120
	maxURL     = 200
	maxSnippet = 300
)

// proxyVars are refused at construction rather than ignored. With a proxy
// configured, http.Transport hands DialContext the PROXY's address, so
// permitAddr would inspect the wrong peer while reporting itself enforced —
// the control would still pass its tests and guard nothing.
var proxyVars = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "all_proxy",
}

// Result is one search hit, already truncated to what a model may see.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Searcher performs the one request this process is permitted to make.
type Searcher struct {
	client *http.Client
	key    string
}

// New builds the searcher, or refuses. There is no disabled Searcher: a nil
// *Searcher means the gate is off and every caller checks for nil, so there
// is no half-configured object that might dial by accident.
func New(listenAddr, apiKey string) (*Searcher, error) {
	for _, v := range proxyVars {
		if os.Getenv(v) != "" {
			return nil, fmt.Errorf("%s is set: a proxy would make every address check inspect the proxy "+
				"instead of the real destination, so the outward gate refuses to start", v)
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("the outward gate is enabled but no credential is configured for " + primaryHost)
	}
	policy, err := newAddrPolicy([]string{listenAddr})
	if err != nil {
		return nil, fmt.Errorf("could not determine this machine's own addresses: %w", err)
	}

	tr := &http.Transport{
		Proxy: nil, // literal, never http.ProxyFromEnvironment
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			// Control runs after resolution and before connect, once per
			// candidate address. It is the only hook where the check and the
			// connect cannot be separated by a re-resolve, which is what
			// makes DNS rebinding and Happy-Eyeballs races unexploitable.
			Control: func(_, address string, _ syscall.RawConn) error {
				return policy.permit(address)
			},
		}).DialContext,
		// Pooling would make the dial-time check a per-connection backstop
		// instead of a per-request gate, so it is switched off and
		// checkAuthority runs on every request regardless.
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
		// No pinned ServerName: there are two permitted destinations and Go
		// derives SNI from the request host, which is what keeps certificate
		// verification honest for each. checkAuthority is what bounds WHICH
		// hosts may appear there.
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	return &Searcher{
		key: apiKey,
		client: &http.Client{
			Transport: guard{next: tr},
			Timeout:   fetchTimeout,
			// A search API that redirects is one worth not using: following
			// a hop would move the destination after the operator approved it.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// guard re-checks the authority on every request, including any the stdlib
// might construct itself.
type guard struct{ next http.RoundTripper }

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := checkAuthority(r.URL.String()); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(r)
}

// Search runs a query, primary destination first.
//
// The primary is always consulted. Only when it has nothing to say — no
// results, or it cannot be reached at all — does the query go to the second
// destination. Both are compiled in, so "search somewhere else" never means
// "somewhere a model or a prompt chose".
//
// Errors are returned WHOLE to the caller, which logs them and hands the model
// a fixed string instead. Go's *url.Error stringifies the full URL and dial
// target, and a tool error lands verbatim in a model's context: that is an
// internal port scanner, handed over politely.
func (s *Searcher) Search(ctx context.Context, query string) (Outcome, error) {
	primary, status, n, err := s.fetchPrimary(ctx, query)
	if err == nil && len(primary) > 0 {
		return Outcome{Results: primary, Host: primaryHost, Status: status, Bytes: n}, nil
	}
	primaryErr := err

	second, status2, n2, err2 := s.fetchFallback(ctx, query)
	if err2 != nil {
		// Both failed. Report BOTH: hiding the second failure behind the
		// first is how a fallback rots unnoticed — it would look like the
		// primary is merely down while the backstop had been broken for
		// months.
		if primaryErr != nil {
			return Outcome{Host: primaryHost, Status: status},
				fmt.Errorf("both search services failed: %w; and then: %v", primaryErr, err2)
		}
		return Outcome{Host: fallbackHost, Status: status2},
			fmt.Errorf("the primary returned nothing and the second failed: %w", err2)
	}
	return Outcome{Results: second, Host: fallbackHost, Status: status2, Bytes: n2, Secondary: true}, nil
}

// Outcome is one completed search: what came back, and from where. The host is
// recorded because an audit that cannot say which party received a query
// cannot answer the question it exists for.
type Outcome struct {
	Results   []Result
	Host      string
	Status    int
	Bytes     int
	Secondary bool
}

func (s *Searcher) fetchPrimary(ctx context.Context, query string) ([]Result, int, int, error) {
	body, status, err := s.get(ctx, WireURL(query), true)
	if err != nil {
		return nil, status, 0, err
	}
	var payload struct {
		Results []Result `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, len(body), fmt.Errorf("%s answered with something that is not the expected JSON: %q",
			primaryHost, head(string(body), 120))
	}
	return truncate(payload.Results), status, len(body), nil
}

func (s *Searcher) fetchFallback(ctx context.Context, query string) ([]Result, int, int, error) {
	body, status, err := s.get(ctx, FallbackWireURL(query), false)
	if err != nil {
		return nil, status, 0, err
	}
	// The shape this endpoint documents. Its coverage is abstracts and
	// related topics rather than a full web index, which is stated plainly
	// rather than dressed up: a thin answer the operator understands beats a
	// rich-looking one that is quietly wrong.
	var payload struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		Heading       string `json:"Heading"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, len(body), fmt.Errorf("the second search service answered with unexpected data")
	}

	var out []Result
	if payload.AbstractText != "" {
		out = append(out, Result{Title: payload.Heading, URL: payload.AbstractURL, Snippet: payload.AbstractText})
	}
	for _, t := range payload.RelatedTopics {
		if t.Text == "" {
			continue
		}
		out = append(out, Result{Title: head(t.Text, 80), URL: t.FirstURL, Snippet: t.Text})
	}
	if len(out) == 0 {
		return nil, status, len(body), nil
	}
	return truncate(out), status, len(body), nil
}

// get performs one request against an already-constructed permitted URL.
func (s *Searcher) get(ctx context.Context, u *url.URL, withKey bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	if withKey {
		// The credential travels in a header, never in a query string: query
		// strings are logged by every intermediary and shown in the audit
		// table. It goes only to the destination that issued it.
		req.Header.Set("X-Api-Key", s.key)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	// CheckRedirect returns ErrUseLastResponse, so a 3xx arrives here as a
	// response rather than an error. Any non-2xx is drained and refused —
	// including redirects, which must never move a destination the operator
	// already approved. 2xx rather than 200 exactly because a real service
	// answers 202 with a complete body, and refusing that would have been a
	// gate that only ever failed.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, readLimit))
		return nil, resp.StatusCode, fmt.Errorf("%s answered with status %d", u.Host, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	return body, resp.StatusCode, err
}

func truncate(in []Result) []Result {
	if len(in) > maxResults {
		in = in[:maxResults]
	}
	for i := range in {
		in[i].Title = clip(in[i].Title, maxTitle)
		in[i].URL = clip(in[i].URL, maxURL)
		in[i].Snippet = clip(in[i].Snippet, maxSnippet)
	}
	return in
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
