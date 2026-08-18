// Package update answers one question — is there a newer release than the one
// running — and answers it without telling anybody anything.
//
// READ THIS BEFORE THE CODE. Everything else this product does at runtime is
// either local or an act somebody took deliberately: no telemetry, no
// analytics, nothing fetched to render a screen. A version check is the first
// outbound request the server makes on its own behalf, and that makes its
// DEFAULT the whole design, not the HTTP.
//
// So the default is neither on nor off. It is ASK: nothing leaves this machine
// until an operator says it may, and the question is put once, in the
// interface, rather than buried in a file nobody opens. An install that has no
// business making outbound requests sets `update_check: off` and is never asked
// and never checks — including when somebody presses "check now".
//
// What goes out, when it does, is a GET to a public releases API with no query
// string, no identifier, no version of anything, and no count. What comes back
// is a tag and the release notes. There is nothing in the request that could
// tell anyone this install exists beyond the fact that an IP asked GitHub a
// public question, and if that ever needs to change it is a separate decision
// made in the open rather than a field added to a request that already exists.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What an install has decided about checking. Three values rather than a bool,
// because "nobody has said yet" is a real state and spelling it as `false`
// would make an unanswered question indistinguishable from a refusal.
const (
	// ModeAsk is the default: no request until somebody answers.
	ModeAsk = "ask"
	// ModeOn checks on a timer and when asked.
	ModeOn = "on"
	// ModeOff never checks, never asks, and refuses "check now". It is the
	// setting for an install that must not make outbound requests at all, so
	// it is honoured without exception rather than being a preference the
	// product may talk itself past.
	ModeOff = "off"
)

// Modes is every value the setting accepts, for the error message that has to
// list them.
var Modes = []string{ModeAsk, ModeOn, ModeOff}

// ValidMode reports whether s is one of them.
func ValidMode(s string) bool {
	for _, m := range Modes {
		if s == m {
			return true
		}
	}
	return false
}

// Where the two halves of a Cogitorium install live. Compiled in rather than
// configurable: a check that could be pointed at another host is a check that
// can be pointed at an attacker's host by anyone who can edit the config, and
// what it fetches is displayed to an operator as release notes.
const (
	repoCogitorium   = "orkcom-tech/cogitorium"
	repoContextverse = "orkcom-tech/contextverse"

	// defaultAPI is the only host this package talks to. It is a constant and
	// there is no setter and no yaml tag: the field on Checker below exists so
	// the tests can point at their own server, and an install cannot.
	defaultAPI = "https://api.github.com"

	// The product names as an operator reads them.
	ProductCogitorium   = "Cogitorium"
	ProductContextverse = "Contextverse"
)

// Every check is bounded twice — once per request and once for the pair — so a
// GitHub that accepts a connection and then says nothing cannot hold a
// goroutine open for the life of the process.
const (
	requestTimeout = 10 * time.Second
	// checkEvery is a day. The question "is there a new release" changes at
	// the speed releases are cut, and anything more often is a request that
	// cannot learn anything new.
	checkEvery = 24 * time.Hour
	// startDelay is how long the first check waits after the server starts.
	// Long enough that boot is not competing with it, short enough that
	// somebody who leaves the tab open sees an answer in the same sitting.
	startDelay = 30 * time.Second
	// notesCap bounds what is kept from a release body. Release notes are
	// somebody else's text rendered in this operator's interface, and an
	// unbounded one is a megabyte in a rail menu.
	notesCap = 8000
)

// Release is the part of a GitHub release this product has any use for.
type Release struct {
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	Notes       string `json:"notes"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
}

// Product is one half of the pair, and what is known about its versions.
//
// Running and Latest are kept apart from Newer deliberately. "There is a 1.6.0
// and you have 1.5.0" and "you should update" are different statements, and the
// second one is only made when the comparison was actually conclusive.
type Product struct {
	Name string `json:"name"`
	// Running is what this machine has. Empty means the product is not
	// installed here — which is an ordinary state for Contextverse and is not
	// an error.
	Running string `json:"running"`
	// Latest is the newest published release, or nil when the check could not
	// reach it.
	Latest *Release `json:"latest,omitempty"`
	// Newer is true only when Running and Latest both parsed and Latest is
	// strictly greater. A development build never sets it: somebody running
	// 0.0.0-dev built it themselves and does not need to be told about a tag.
	Newer bool `json:"newer"`
	// Comparable is false when Running could not be read as a release version.
	// It is the difference between "you are up to date" and "nothing here can
	// say", and collapsing the two would let a dev build claim currency it has
	// no basis for.
	Comparable bool `json:"comparable"`
	// Error is why this half has no answer. It is shown, not swallowed: an
	// operator who turned the check on and sees nothing deserves to know
	// whether that means "current" or "could not ask".
	Error string `json:"error,omitempty"`
}

// Report is the whole answer, as the interface receives it.
type Report struct {
	Mode string `json:"mode"`
	// CheckedAt is when the last completed check ran. Zero means none has.
	CheckedAt time.Time `json:"checked_at"`
	Products  []Product `json:"products"`
	// Install is how this copy got onto the machine, which decides what an
	// honest "how to take it" line can say. See Install.
	Install Installed `json:"install"`
}

// Any reports whether anything in this report has moved on.
func (r Report) Any() bool {
	for _, p := range r.Products {
		if p.Newer {
			return true
		}
	}
	return false
}

// Checker holds the last answer and the permission to go and get another.
//
// The cached report is the point: the interface asks this server, and this
// server asks GitHub once a day. A rail that polled a public API on every page
// load would be rate-limited within an hour of a team opening it.
type Checker struct {
	mu     sync.Mutex
	mode   string
	last   Report
	client *http.Client

	// running is this binary's version, and contextd is a function because the
	// answer requires running a subprocess and must not be taken at startup —
	// an install that has no contextd should not pay for discovering that on
	// every boot.
	running  string
	contextd func(context.Context) string

	// base is defaultAPI everywhere but in this package's own tests. Not
	// exported, not configurable, and not read from anywhere outside this
	// file — see defaultAPI.
	base string

	// remember is where the operator's answer survives a restart. Nil is a
	// working checker that forgets — which is what every test and any
	// embedding gets — but on a real install it means the product asks the
	// same question after every restart and looks like it never listened.
	remember Settings

	// ceiling is what the configuration permits, and it never changes. mode
	// may move under it; it may not move above it. Kept separately from mode
	// because "off in the file" and "off because somebody answered no" are
	// the same value with different authority, and only one of them can be
	// undone from a browser.
	ceiling string

	// The timer's lifetime. ctx is the server's, handed in by Start; live
	// counts the loops actually running, so answering "on" twice cannot leave
	// two goroutines asking GitHub on two different days. A COUNT rather than
	// a bool because a bool cannot tell "one" from "two" — which is precisely
	// the bug it would be guarding against.
	ctx  context.Context
	live int
}

// Settings is where an answer is remembered. An interface rather than the
// store itself, so this package keeps its one dependency — net/http — and a
// test can run without a database.
type Settings interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// SettingKey is the row this package owns.
const SettingKey = "update_check"

// New builds a checker. mode is the configured setting; an unknown value is
// treated as ModeOff and logged, because the safe reading of a setting nobody
// can parse is the one that makes no requests.
func New(mode, running string, contextd func(context.Context) string) *Checker {
	if !ValidMode(mode) {
		slog.Error("update_check is not a value this server knows; treating it as off",
			"value", mode, "known", strings.Join(Modes, ", "))
		mode = ModeOff
	}
	if contextd == nil {
		contextd = func(context.Context) string { return "" }
	}
	c := &Checker{
		mode:     mode,
		ceiling:  mode,
		running:  running,
		contextd: contextd,
		base:     defaultAPI,
	}
	c.client = &http.Client{
		Timeout: requestTimeout,
		// No redirects off the host this check is pointed at. The URL is
		// compiled in, and a releases API that wants to send this server
		// somewhere else is not answering the question it was asked — it is
		// choosing where this operator's release notes come from.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			want, err := url.Parse(c.base)
			if err != nil || r.URL.Host != want.Host {
				return fmt.Errorf("the releases API redirected to %s, which is not where this check is allowed to go", r.URL.Host)
			}
			return nil
		},
	}
	return c
}

// Mode returns the current setting.
func (c *Checker) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// SetMode records an answer to the question the interface asked.
//
// It cannot leave ModeOff. An install whose configuration says it must not make
// outbound requests does not acquire the ability because somebody clicked a
// button in a browser: the operator who set it edited a file on the server's
// own disk, and a UI that could undo that would make the file a suggestion.
func (c *Checker) SetMode(ctx context.Context, mode string) error {
	if !ValidMode(mode) {
		return fmt.Errorf("update_check may be %s", strings.Join(Modes, ", "))
	}

	c.mu.Lock()
	if c.mode == ModeOff {
		c.mu.Unlock()
		return errors.New("update checking is switched off in this server's configuration, " +
			"which is a decision made on the server's own disk: change update_check there and restart")
	}
	if c.mode == mode {
		c.mu.Unlock()
		return nil
	}
	c.mode = mode
	if mode != ModeOn {
		// Whatever was found under the old setting is not shown under the new
		// one. Leaving a stale "1.6.0 is out" on screen after somebody
		// switched the check off is the product ignoring the answer it asked
		// for.
		c.last = Report{}
	}
	remember := c.remember
	c.mu.Unlock()

	slog.Info("update checking set", "mode", mode)

	// Remembered, or the question comes back after every restart and the
	// product looks like it never listened. A store that refuses is logged and
	// not fatal: the setting holds for this process either way, and failing an
	// operator's click because a write failed would be the worse trade.
	if remember != nil {
		if err := remember.Set(context.WithoutCancel(ctx), SettingKey, mode); err != nil {
			slog.Error("the update-check answer could not be stored; it will be asked again after a restart", "err", err)
		}
	}

	// Answering "on" starts the timer that "ask" never had one for. Without
	// this the operator gets one check and then silence until a restart — see
	// tick, which takes the lock itself and is why it is called after the
	// unlock rather than inside the section above.
	//
	// A FULL interval before the first automatic pass, not zero: the caller
	// that just took this answer does its own immediate check so the operator
	// sees the result of the thing they just agreed to, and a loop starting at
	// zero would make that two GETs a second apart.
	c.tick(checkEvery)
	return nil
}

// Load applies the answer stored on a previous run, and is where the ceiling is
// enforced on the way in.
//
// A stored "on" under a configured "off" is ignored and said so. That is the
// case that matters: an install that was checking daily, and whose operator has
// since edited the config file to stop it, must stop — the file wins, and a row
// written before it was edited must not quietly outrank it.
func (c *Checker) Load(ctx context.Context, s Settings) {
	if s == nil {
		return
	}
	c.mu.Lock()
	c.remember = s
	ceiling := c.ceiling
	c.mu.Unlock()

	stored, err := s.Get(ctx, SettingKey)
	if err != nil {
		slog.Error("could not read the stored update-check answer; using the configured one", "err", err)
		return
	}
	if stored == "" {
		return
	}
	if !ValidMode(stored) {
		slog.Error("the stored update-check answer is not a value this server knows; ignoring it", "value", stored)
		return
	}
	if ceiling == ModeOff {
		if stored != ModeOff {
			slog.Warn("a stored update-check answer is outranked by this server's configuration",
				"stored", stored, "configured", ModeOff)
		}
		return
	}
	c.mu.Lock()
	c.mode = stored
	c.mu.Unlock()
	slog.Info("update checking restored from the stored answer", "mode", stored)
}

// Report is what the interface reads. It never triggers a request: a screen
// opening must not put a packet on the wire.
func (c *Checker) Report() Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.last
	r.Mode = c.mode
	r.Install = Install()
	if r.Products == nil {
		r.Products = []Product{}
	}
	return r
}

// Check asks, now, and stores the answer.
//
// Refused when the setting is off — including for "check now", which is the
// whole meaning of "honoured without exception". When the setting is ask, a
// deliberate press IS the answer for this one look, and nothing is remembered:
// somebody who wanted to see today has not agreed to a daily request.
func (c *Checker) Check(ctx context.Context) (Report, error) {
	c.mu.Lock()
	mode := c.mode
	c.mu.Unlock()
	if mode == ModeOff {
		return Report{Mode: ModeOff}, errors.New("update checking is switched off in this server's configuration")
	}

	products := []Product{
		c.one(ctx, ProductCogitorium, repoCogitorium, c.running),
		c.one(ctx, ProductContextverse, repoContextverse, c.contextd(ctx)),
	}
	r := Report{Mode: mode, CheckedAt: time.Now().UTC(), Products: products, Install: Install()}

	c.mu.Lock()
	c.last = r
	c.mu.Unlock()

	for _, p := range products {
		switch {
		case p.Error != "":
			// Info, not Error: a laptop that is off the network is the ordinary
			// case, and a stack of ERROR lines about it every day is noise
			// about something the operator already knows.
			slog.Info("could not check for a newer release", "product", p.Name, "err", p.Error)
		case p.Newer:
			slog.Info("a newer release exists", "product", p.Name, "running", p.Running, "latest", p.Latest.Tag)
		}
	}
	return r, nil
}

// one checks a single product. Its error is returned inside the Product rather
// than as an error, because half an answer is worth having: Contextverse being
// unreachable is not a reason to say nothing about Cogitorium.
func (c *Checker) one(ctx context.Context, name, repo, running string) Product {
	p := Product{Name: name, Running: strings.TrimSpace(running)}
	if p.Running == "" && name == ProductContextverse {
		// Not installed. Saying "there is a 1.0.0 of a thing you do not have"
		// is an advertisement rather than an update notice, and this product
		// does not run those.
		return p
	}

	rel, err := c.latest(ctx, repo)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Latest = rel

	have, okHave := parse(p.Running)
	want, okWant := parse(rel.Tag)
	p.Comparable = okHave && okWant
	p.Newer = p.Comparable && want.after(have)
	return p
}

// githubRelease is the shape of the one endpoint this package calls. Named
// fields rather than a map, so a change in that API is a compile-time or
// decode-time fact rather than a nil deref in a rail menu.
type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

func (c *Checker) latest(ctx context.Context, repo string) (*Release, error) {
	endpoint := c.base + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// A user agent because GitHub requires one, and this one deliberately
	// carries no version and no install identifier: it names the product
	// asking, which is the least that can be sent and still be answered.
	req.Header.Set("User-Agent", "cogitorium")

	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the releases API: %w", err)
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("that repository has no published releases yet")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, errors.New("the releases API is rate-limiting this address; the next check is a day away")
	default:
		return nil, fmt.Errorf("the releases API answered %s", res.Status)
	}

	// Bounded read. A response body from anywhere outside this machine is
	// hostile until it has a size.
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("could not read the releases API answer: %w", err)
	}
	var gr githubRelease
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("the releases API answered something this server could not read: %w", err)
	}
	if gr.Draft || gr.Prerelease {
		// /releases/latest does not return these, so reaching here means the
		// API changed. Refusing is better than telling an operator to install
		// a prerelease.
		return nil, errors.New("the newest release is a draft or a prerelease, which this check does not offer")
	}
	if gr.TagName == "" {
		return nil, errors.New("the releases API answered with no tag")
	}
	notes := gr.Body
	if len(notes) > notesCap {
		notes = notes[:notesCap] + "\n\n… truncated; the rest is on the release page."
	}
	return &Release{
		Tag: gr.TagName, Name: gr.Name, Notes: notes,
		URL: gr.HTMLURL, PublishedAt: gr.PublishedAt,
	}, nil
}

// Start runs the check on a timer until ctx ends.
//
// It returns immediately and the first check is deferred, because STARTUP MUST
// NOT WAIT FOR THIS. A slow or unreachable GitHub is not a reason for a server
// to be slow to listen, and a check that ran inline would make somebody else's
// availability part of this product's boot.
func (c *Checker) Start(ctx context.Context) {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
	// A short first delay: a server that has just started is doing more useful
	// things, and nobody is looking at a rail in the first half minute.
	c.tick(startDelay)
}

// tick makes sure a loop is running, if one should be.
//
// THIS IS WHAT SetMode CALLS, and the reason it exists. Start runs once, at
// construction, when the setting is usually still "ask" — so an operator who
// then answered "on" in the interface used to get exactly one check and silence
// until the process was restarted. Answering a question must start the thing
// the question was about.
//
// Idempotent on purpose: answering "on" twice, or a Start followed by an
// answer, must not leave two goroutines asking GitHub on two different days.
func (c *Checker) tick(first time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Nothing is scheduled at all under ask or off. A timer that woke up to
	// decide not to check would still be a timer, and the point of ask is that
	// no machinery runs until somebody says yes.
	if c.mode != ModeOn || c.live > 0 || c.ctx == nil {
		return
	}
	c.live++
	ctx := c.ctx
	go c.loop(ctx, first)
}

// isTicking and tickers exist for this package's own tests, which are the only
// place that can see the invariant they check: exactly one loop, started when
// the answer is given rather than only at construction. Unexported, so nothing
// outside can build behaviour on the timer's internals.
func (c *Checker) tickers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

func (c *Checker) loop(ctx context.Context, first time.Duration) {
	defer func() {
		c.mu.Lock()
		c.live--
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return
	case <-time.After(first):
	}
	for {
		// Re-read every time round rather than trusting the value this loop
		// started under: an operator who switched the check off an hour ago
		// must not get one more request tomorrow morning.
		if c.Mode() != ModeOn {
			return
		}
		if _, err := c.Check(ctx); err != nil {
			slog.Info("update check refused", "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkEvery):
		}
	}
}

// ── versions ──────────────────────────────────────────────────────────────

// version is the smallest comparison that answers this question honestly.
//
// Not a semver library: the only versions compared here are tags this project
// cuts and the version its own binary was stamped with, both of the form
// v1.5.0. Anything else fails to parse, and failing to parse is a REPORTED
// state rather than a silent zero — see Product.Comparable.
type version struct{ major, minor, patch int }

func (v version) after(o version) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	if v.minor != o.minor {
		return v.minor > o.minor
	}
	return v.patch > o.patch
}

func parse(s string) (version, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// A build stamped 1.5.0-4-gdeadbee or a prerelease 1.6.0-rc1 is not
	// something this compares. Cutting at the first dash and comparing the
	// release part would say a release candidate is the release.
	if s == "" || strings.ContainsAny(s, "-+") {
		return version{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		switch i {
		case 0:
			v.major = n
		case 1:
			v.minor = n
		case 2:
			v.patch = n
		}
	}
	return v, true
}

// ── how this copy got here ────────────────────────────────────────────────

// Installed says how the binary was installed and therefore what an honest
// "take the update" line can offer.
//
// THIS IS THE HALF THAT IS USUALLY GOT WRONG. A self-updater that overwrites a
// file Homebrew believes it owns produces a machine nobody can reason about:
// `brew list` says one version, the binary is another, and the next `brew
// upgrade` silently reverts it. So this product never writes over its own
// binary. It works out who owns it and prints THAT owner's command.
type Installed struct {
	// Kind is one of: homebrew, scoop, winget, deb-rpm, container, kubernetes,
	// desktop, manual.
	Kind string `json:"kind"`
	// Command is what the operator should run, or empty when there is no
	// honest one — in a container or on Kubernetes the deploy pipeline owns
	// the version and a command typed into a pod is undone by the next roll.
	Command string `json:"command"`
	// Note is the sentence shown beside it.
	Note string `json:"note"`
}

// Install works out how this copy got onto the machine.
//
// Every branch is a fact about the filesystem or the environment, never a build
// flag: a binary built once and installed three ways would carry the same flag
// in all three. Unknown is a real answer and says so rather than guessing at a
// package manager that may not be there.
func Install() Installed {
	// Kubernetes first: a pod is also a container, and the more specific
	// answer is the useful one.
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return Installed{
			Kind: "kubernetes",
			Note: "This install is a pod, so its version belongs to whatever applies the chart. " +
				"Update the image tag in your values and roll it — nothing typed in here would survive.",
		}
	}
	if inContainer() {
		return Installed{
			Kind: "container",
			Note: "This install is a container, so its version is the image tag. " +
				"Pull the new one and recreate it — a binary replaced inside a running container is gone on the next start.",
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return Installed{Kind: "manual", Note: "Replace the binary with the one from the release page."}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		// Homebrew puts a symlink in bin and the real file in the Cellar, so
		// the unresolved path never says brew and the resolved one always does.
		exe = resolved
	}
	p := filepath.ToSlash(exe)
	lower := strings.ToLower(p)

	switch {
	case strings.Contains(lower, "/cellar/") || strings.Contains(lower, "/homebrew/"):
		return Installed{
			Kind:    "homebrew",
			Command: "brew upgrade cogitorium",
			Note:    "Homebrew owns this binary, so Homebrew replaces it.",
		}
	case strings.Contains(lower, "/scoop/"):
		return Installed{
			Kind:    "scoop",
			Command: "scoop update cogitorium",
			Note:    "Scoop owns this binary, so Scoop replaces it.",
		}
	case strings.Contains(lower, "/winget"), strings.Contains(lower, "\\winget"),
		strings.Contains(lower, "/windowsapps/"):
		return Installed{
			Kind:    "winget",
			Command: "winget upgrade OrkcomTech.Cogitorium",
			Note:    "winget owns this binary, so winget replaces it.",
		}
	case strings.HasPrefix(p, "/usr/bin/"), strings.HasPrefix(p, "/usr/local/bin/"), strings.HasPrefix(p, "/opt/"):
		return Installed{
			Kind: "deb-rpm",
			Note: "This looks like a system package. Update it with the package manager that put it there " +
				"(apt, dnf or zypper) rather than by replacing the file.",
		}
	}
	return Installed{
		Kind: "manual",
		Note: "This binary was put here by hand, so replacing it is by hand too: " +
			"download the new one from the release page and swap it while the server is stopped.",
	}
}

// inContainer is the two checks that are true of a container and of nothing
// else on an ordinary machine. Not exhaustive, and it does not need to be:
// getting this wrong shows the operator a slightly wrong sentence, not a wrong
// action, because no branch of this ever writes anything.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		return strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods")
	}
	return false
}
