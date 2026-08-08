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
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	// fetchTimeout is deliberately shorter than the server's shutdown grace,
	// so a fetch can never outlive Shutdown and strand an awaiting audit row.
	fetchTimeout = 8 * time.Second

	// readLimit guards the JSON parser. It is not the budget the model sees —
	// that is maxResults below, applied after parsing. Without the second
	// cap, web_search would be web_fetch with an extra hop.
	readLimit  = 3 << 10
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
		return nil, errors.New("the outward gate is enabled but no credential is configured for " + searchHost)
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
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: searchHost,
		},
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

// Search issues the one permitted request and returns the results, the HTTP
// status, and the bytes read. Errors are returned whole to the CALLER, which
// logs them; the caller is responsible for handing the model a fixed string
// instead, because Go's *url.Error stringifies the full URL and dial target
// and would turn a tool error into an internal port scanner.
func (s *Searcher) Search(ctx context.Context, query string) ([]Result, int, int, error) {
	u := WireURL(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, 0, err
	}
	// The credential travels in a header, never in the query string: query
	// strings are logged by every intermediary and shown in the audit table.
	req.Header.Set("X-Api-Key", s.key)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()

	// CheckRedirect returns ErrUseLastResponse, so a 3xx arrives here as a
	// response rather than an error. Anything but 200 is drained and refused.
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, readLimit))
		return nil, resp.StatusCode, 0, fmt.Errorf("%s answered with status %d", searchHost, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return nil, resp.StatusCode, len(body), err
	}

	// The expected shape. echo-page.com is unreleased as of this writing, so
	// this contract is asserted rather than observed: if the service answers
	// with something else the call fails loudly with the body's opening
	// bytes, which is the honest outcome — there is no fallback parse and no
	// "best effort" reading that would quietly hand the model garbage.
	var payload struct {
		Results []Result `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		snippet := string(body)
		if len(snippet) > 120 {
			snippet = snippet[:120]
		}
		return nil, resp.StatusCode, len(body),
			fmt.Errorf("%s answered with something that is not the expected JSON: %q", searchHost, snippet)
	}

	out := payload.Results
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	for i := range out {
		out[i].Title = clip(out[i].Title, maxTitle)
		out[i].URL = clip(out[i].URL, maxURL)
		out[i].Snippet = clip(out[i].Snippet, maxSnippet)
	}
	return out, resp.StatusCode, len(body), nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
