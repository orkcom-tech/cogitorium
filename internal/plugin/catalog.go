package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The catalog: where somebody finds a plugin they did not write.
//
// One file, five fields per entry, and the artifacts live in the author's own
// GitHub releases rather than anywhere this project hosts. That is deliberate
// and it is the whole reason a catalog is cheap to run: the index is a list of
// pointers, so it costs one small JSON file no matter how many plugins there
// are, and nobody has to store or serve somebody else's binaries.
//
// The index says where things are. It never says what is true — a downloaded
// JSON file asserting that a plugin is trustworthy would be a plugin asserting
// it about itself, one indirection away. Trust is a separate statement, signed
// by an identity the client has compiled in, and it is checked against bytes on
// disk rather than read out of this list.

// CatalogURL is where the published index lives.
//
// Compiled in rather than configured, for the same reason the update check's
// endpoint is: an index an attacker can point somewhere else is not an index,
// and an operator who wants a different one is describing a fork.
const CatalogURL = "https://raw.githubusercontent.com/orkcom-tech/cogitorium-plugins/main/plugins.json"

// Entry is one plugin as the catalog lists it.
//
// Five fields, and no more on purpose. Everything else about a plugin — what
// it needs, what it overrides, what it asks for — is in its own manifest,
// where it travels with the code rather than with a description somebody wrote
// once and never updated.
type Entry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Description string `json:"description"`
	// Repo is "owner/name" on GitHub. The bundle is fetched from its releases,
	// so an author publishes by tagging rather than by asking anybody.
	Repo string `json:"repo"`

	// Version is the current release, filled in by the catalog's CI rather
	// than by the author. Absent in a hand-written plugins.json, which is why
	// nothing depends on it being there.
	Version string `json:"version,omitempty"`

	// Cover is one picture for the library, and it is NOT a free URL.
	//
	// An arbitrary address here would make browsing the library a request to
	// whatever host an author named — which tells them who is looking, from
	// where, and when, before anybody has installed anything. So it is pinned
	// to the author's own repository on the same raw host the catalog itself
	// is read from: no new party learns anything that reading the catalog did
	// not already tell them.
	//
	// Written by the author in their own submission — unlike Version, which
	// the catalog's CI fills in. Nothing needs to fill this one, because the
	// pin is what makes it safe rather than the process: an entry naming a
	// tracker fails validation here, in the client, whatever the submission
	// process did or did not check.
	Cover string `json:"cover,omitempty"`

	// bundleBase overrides where the bundle is fetched from. Unexported and
	// never decoded from JSON, so a catalog entry cannot redirect a download
	// somewhere the convention does not point. It exists for tests.
	bundleBase string
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// Validate checks one entry. Run by the catalog's CI on a submission and again
// here, because a client that trusted the shape of what it downloaded would be
// trusting the submission process to have run.
func (e Entry) Validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("an entry needs an id")
	case !idRe.MatchString(e.ID) || len(e.ID) < 3 || len(e.ID) > 48:
		return fmt.Errorf("%q is not a usable plugin id", e.ID)
	case reservedIDs[e.ID]:
		return fmt.Errorf("%q is reserved by the host", e.ID)
	case e.Name == "":
		return fmt.Errorf("%s: an entry needs a name", e.ID)
	case e.Author == "":
		return fmt.Errorf("%s: an entry needs an author", e.ID)
	case e.Description == "":
		return fmt.Errorf("%s: an entry needs a description", e.ID)
	case !repoRe.MatchString(e.Repo):
		return fmt.Errorf("%s: repo must be owner/name, got %q", e.ID, e.Repo)
	case e.Cover != "" && !strings.HasPrefix(e.Cover, e.coverPrefix()):
		return fmt.Errorf("%s: cover must be a file in %s, got %q — a picture on somebody "+
			"else's host would tell them who is browsing the library", e.ID, e.Repo, e.Cover)
	case e.Cover != "" && MediaKind(e.Cover) != "image":
		return fmt.Errorf("%s: cover is %q, which is not a picture", e.ID, e.Cover)
	}
	return nil
}

// coverPrefix is where a cover for this entry may live: the author's own
// repository, on the host the catalog is already read from.
func (e Entry) coverPrefix() string {
	return "https://raw.githubusercontent.com/" + e.Repo + "/"
}

// BundleURL is where this entry's bundle is fetched from.
//
// Built by convention rather than by asking GitHub's API: the API needs a
// token to be useful at any volume, rate-limits without one, and would make
// the catalog depend on a service being up to answer a question the URL
// already answers.
func (e Entry) BundleURL(version string) string {
	if e.bundleBase != "" {
		return e.bundleBase
	}
	if version == "" || version == "latest" {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download/%s.zip", e.Repo, e.ID)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s.zip", e.Repo, version, e.ID)
}

// SourceURL is where a person goes to read the code before approving it, which
// is the step this whole product's trust story rests on.
func (e Entry) SourceURL() string { return "https://github.com/" + e.Repo }

// Index is the published list.
type Index struct {
	Entries []Entry
	// VerifiedList is what the team has read. Kept beside the entries rather
	// than folded into them, because it is a different statement made by
	// different people through a different path into the same repository.
	VerifiedList []Verified
	// Fetched is when this copy was taken. Shown wherever the catalog is,
	// because a cached list is not a current one and pretending otherwise is
	// how somebody installs a version that was yanked yesterday.
	Fetched time.Time
	// Cached reports that this came off disk rather than the network.
	Cached bool
}

// Find returns one entry.
func (i Index) Find(id string) (Entry, bool) {
	for _, e := range i.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Search matches on what a person actually types: a name, an author, or a word
// from the description.
func (i Index) Search(q string) []Entry {
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]Entry, 0, len(i.Entries))
	for _, e := range i.Entries {
		if q == "" || strings.Contains(strings.ToLower(
			e.ID+" "+e.Name+" "+e.Author+" "+e.Description), q) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Catalog fetches and caches the index.
type Catalog struct {
	dataDir     string
	url         string
	derivedURL  string
	verifiedURL string
	client      *http.Client
	// allow is the operator's egress consent, the same gate the update check
	// and the MCP registry already answer to. A browse is a network call and
	// it asks nobody's permission twice.
	allow func() bool
}

// NewCatalog prepares the catalog.
func NewCatalog(dataDir string, client *http.Client, allow func() bool) *Catalog {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if allow == nil {
		allow = func() bool { return false }
	}
	return &Catalog{
		dataDir: dataDir, url: CatalogURL, derivedURL: DerivedURL, verifiedURL: VerifiedURL,
		client: client, allow: allow,
	}
}

func (c *Catalog) cachePath() string { return filepath.Join(c.dataDir, "plugins-catalog.json") }

// Fetch gets the current index, falling back to the cached copy.
//
// The fallback is not a silent one: the result says it is cached and when it
// was taken, so a screen can show that rather than presenting yesterday's list
// as today's.
func (c *Catalog) Fetch(ctx context.Context) (Index, error) {
	if !c.allow() {
		idx, err := c.cached()
		if err != nil {
			return Index{}, fmt.Errorf("this install is not permitted to reach the plugin "+
				"catalog and has no cached copy. It would have come from %s", c.url)
		}
		return idx, nil
	}

	// The derived index first, because it carries versions; the hand-written
	// list is the fallback when CI has not published one yet.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.derivedURL, nil)
	if err != nil {
		return Index{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if idx, cerr := c.cached(); cerr == nil {
			return idx, nil
		}
		return Index{}, fmt.Errorf("the plugin catalog could not be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// No derived index published yet. The hand-written list still lists
		// every plugin; it simply cannot say which versions exist.
		resp.Body.Close()
		if plain, err := c.fetchPlain(ctx); err == nil {
			return plain, nil
		}
	}
	if resp.StatusCode != http.StatusOK {
		if idx, cerr := c.cached(); cerr == nil {
			return idx, nil
		}
		return Index{}, fmt.Errorf("the plugin catalog answered %s", resp.Status)
	}

	var entries []Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		if idx, cerr := c.cached(); cerr == nil {
			return idx, nil
		}
		return Index{}, fmt.Errorf("the plugin catalog is not a list this build understands: %w", err)
	}

	entries = keepValid(entries)
	verified := c.fetchVerified(ctx)
	if err := c.store(entries, verified); err != nil {
		// Failing to cache is not failing to browse. An install on a read-only
		// filesystem should still be able to look.
		return Index{Entries: entries, VerifiedList: verified, Fetched: time.Now().UTC()}, nil
	}
	return Index{Entries: entries, VerifiedList: verified, Fetched: time.Now().UTC()}, nil
}

// fetchPlain reads the hand-written list, for a catalog whose CI has not
// published a derived index.
func (c *Catalog) fetchPlain(ctx context.Context) (Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return Index{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return Index{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Index{}, fmt.Errorf("the plugin catalog answered %s", resp.Status)
	}
	var entries []Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return Index{}, err
	}
	entries = keepValid(entries)
	verified := c.fetchVerified(ctx)
	_ = c.store(entries, verified)
	return Index{Entries: entries, VerifiedList: verified, Fetched: time.Now().UTC()}, nil
}

// keepValid drops entries this build cannot use rather than refusing the whole
// list.
//
// A catalog that grows a field an older client does not know must not make
// that client unable to browse at all — and one bad row somebody merged must
// not take the other nine hundred with it.
func keepValid(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Validate() != nil || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}

type cachedIndex struct {
	Entries  []Entry    `json:"entries"`
	Verified []Verified `json:"verified,omitempty"`
	Fetched  time.Time  `json:"fetched"`
}

func (c *Catalog) store(entries []Entry, verified []Verified) error {
	b, err := json.Marshal(cachedIndex{Entries: entries, Verified: verified, Fetched: time.Now().UTC()})
	if err != nil {
		return err
	}
	return writeFileAtomic(c.cachePath(), b)
}

func (c *Catalog) cached() (Index, error) {
	b, err := os.ReadFile(c.cachePath())
	if err != nil {
		return Index{}, err
	}
	var ci cachedIndex
	if err := json.Unmarshal(b, &ci); err != nil {
		return Index{}, err
	}
	return Index{
		Entries: keepValid(ci.Entries), VerifiedList: ci.Verified,
		Fetched: ci.Fetched, Cached: true,
	}, nil
}

// ── installing from the catalog ───────────────────────────────────────────

// maxCatalogBundle bounds what will be downloaded. A plugin is templates and
// maybe a module; anything approaching this is a mistake or an attempt to fill
// somebody's disk, and a length nobody checked is how that succeeds.
const maxCatalogBundle = 64 << 20

// Download fetches an entry's bundle to a temporary file.
//
// The URL comes from the entry rather than from anything a response said: a
// redirect chain is followed by the HTTP client, but the thing being asked for
// is always what the catalog pointed at, never what a page suggested.
func (c *Catalog) Download(ctx context.Context, e Entry, version string) (path string, err error) {
	if !c.allow() {
		return "", fmt.Errorf("this install is not permitted to reach the network, so %s cannot "+
			"be downloaded. It would have come from %s", e.ID, e.BundleURL(version))
	}
	url := e.BundleURL(version)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", e.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The commonest real failure by far: the catalog lists a plugin whose
		// author has not attached a bundle to their release, or named it
		// something else. Said in those words, because "404" sends somebody to
		// look at their own network.
		return "", fmt.Errorf("%s answered %s. The catalog expects a release asset named %s.zip "+
			"on %s — the author may not have attached one",
			url, resp.Status, e.ID, e.SourceURL())
	}

	tmp, err := os.CreateTemp("", "cogitorium-plugin-*.zip")
	if err != nil {
		return "", err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxCatalogBundle+1))
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", e.ID, err)
	}
	if n > maxCatalogBundle {
		return "", fmt.Errorf("%s is larger than the %d MB download limit", e.ID, maxCatalogBundle>>20)
	}
	return tmp.Name(), nil
}

// InstallFromCatalog downloads an entry and installs it.
//
// It arrives switched off and unapproved, exactly like every other way a
// plugin gets onto this machine. Coming from a catalog is not a decision
// somebody made about it — it is a decision somebody made about listing it,
// which is a different thing and belongs to a different person.
func (c *Catalog) InstallFromCatalog(ctx context.Context, s *Store, e Entry, version string) (Installed, string, error) {
	path, err := c.Download(ctx, e, version)
	if err != nil {
		return Installed{}, "", err
	}
	defer os.Remove(path)

	in, digest, err := s.Install(path)
	if err != nil {
		return Installed{}, digest, err
	}
	// The catalog and the bundle have to agree about what this is. They are
	// written by the same author but at different times, and a mismatch means
	// one of them is stale — installing it under the catalog's name would put
	// a plugin on disk under an id its own manifest does not claim.
	if in.Manifest.ID != e.ID {
		_ = s.Remove(in.Manifest.ID)
		return Installed{}, digest, fmt.Errorf("the catalog lists this as %q and the bundle says "+
			"it is %q — one of them is out of date, and nothing was installed", e.ID, in.Manifest.ID)
	}
	return in, digest, nil
}

// ── the verified list ─────────────────────────────────────────────────────

// VerifiedURL is the list of plugins somebody on the team has read.
//
// A second file in the same repository, and the whole mechanism is who may
// merge it: entries land through CODEOWNERS review rather than through the
// auto-merge that ordinary submissions use. An author can add themselves to
// plugins.json without waiting for anybody; nobody can add themselves here.
//
// No signatures, no keys, no transparency log. Those defend against somebody
// who controls the repository, and if that has happened the client is fetching
// whatever they want anyway — including a different list of pinned keys in the
// next release. GitHub's own access control is the mechanism, and pretending
// otherwise would be cryptography as decoration.
const VerifiedURL = "https://raw.githubusercontent.com/orkcom-tech/cogitorium-plugins/main/verified.json"

// Verified is one plugin the team has looked at.
type Verified struct {
	ID string `json:"id"`
	// Version is what was actually read. Optional, and worth recording: a
	// plugin is not a fixed thing, and "we checked 1.2.0" beside an installed
	// 1.4.0 is a more useful sentence than a badge that says nothing about
	// which code anybody saw.
	Version string `json:"version,omitempty"`
	// By is who looked, and Note is anything they want to say about it.
	By   string `json:"by,omitempty"`
	Note string `json:"note,omitempty"`
}

// Check is what a client concludes about one plugin.
//
// Three states rather than a boolean, because "we read this exact version",
// "we read an older one" and "nobody has looked" are three different things to
// tell somebody deciding whether to approve it.
type Check struct {
	// State is "verified", "verified-other-version", or "unchecked".
	State string `json:"state"`
	// Version is what the team read, when they said.
	Version string `json:"version,omitempty"`
	By      string `json:"by,omitempty"`
	Note    string `json:"note,omitempty"`
}

const (
	// CheckVerified means the team read the version in question.
	CheckVerified = "verified"
	// CheckOtherVersion means they read a different one. Deliberately not
	// "verified": a badge that survives a version change is a badge about a
	// name rather than about code.
	CheckOtherVersion = "verified-other-version"
	// CheckUnchecked means nobody on the team has looked. It is the ordinary
	// state and it is not an accusation — most plugins will be here.
	CheckUnchecked = "unchecked"
)

// Verify reports what the team said about a plugin at a version.
func (i Index) Verify(id, version string) Check {
	for _, v := range i.VerifiedList {
		if v.ID != id {
			continue
		}
		switch {
		case v.Version == "" || version == "" || v.Version == version:
			return Check{State: CheckVerified, Version: v.Version, By: v.By, Note: v.Note}
		default:
			return Check{State: CheckOtherVersion, Version: v.Version, By: v.By, Note: v.Note}
		}
	}
	return Check{State: CheckUnchecked}
}

// fetchVerified reads the list. A failure here is not a failure to browse: the
// catalog is still usable without it, and every plugin simply reads as
// unchecked — which is true, rather than a guess in either direction.
func (c *Catalog) fetchVerified(ctx context.Context) []Verified {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.verifiedURL, nil)
	if err != nil {
		return nil
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var list []Verified
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil
	}
	out := make([]Verified, 0, len(list))
	for _, v := range list {
		if v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// ── update discovery ──────────────────────────────────────────────────────

// The derived index, and why there is one.
//
// A catalog entry carries no version: an author publishes by tagging a release
// and never touches this repository again, which is the whole reason
// submitting is cheap. But a client cannot know an update exists without a
// version from somewhere.
//
// The alternative is asking GitHub once per installed plugin, and that is the
// thing worth avoiding — not because GitHub is untrustworthy, but because it
// would mean every install continuously telling a third party exactly which
// plugins it runs. So the catalog's own CI does the polling once, for
// everybody, and publishes the answers in one file. A client fetches the WHOLE
// file with no query string, no install id and no list of what it has, and
// diffs locally. What it runs stays its own business by construction rather
// than by policy.

// DerivedURL is the index the catalog's CI publishes: the same entries with
// the current version of each filled in.
const DerivedURL = "https://raw.githubusercontent.com/orkcom-tech/cogitorium-plugins/main/index.json"

// Update is one plugin with something newer available.
type Update struct {
	Entry     Entry
	Installed string
	Available string
}

// Updates compares what is installed against what the index says exists.
//
// Computed here rather than asked for: nothing leaves this machine to answer
// it, because the whole list already arrived.
func (i Index) Updates(installed []Installed) []Update {
	have := map[string]string{}
	for _, in := range installed {
		if in.Broken == nil && !in.Dev {
			have[in.ID] = in.Version
		}
	}
	var out []Update
	for _, e := range i.Entries {
		cur, ok := have[e.ID]
		if !ok || e.Version == "" || e.Version == cur {
			continue
		}
		// Newer only. A catalog that briefly lists an older version — a yank,
		// a bad publish — must not offer somebody a downgrade they did not ask
		// for, and "different" is not the same as "newer".
		a, aok := parseVersion(cur)
		b, bok := parseVersion(e.Version)
		if aok && bok && b.Compare(a) <= 0 {
			continue
		}
		out = append(out, Update{Entry: e, Installed: cur, Available: e.Version})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Entry.Name < out[b].Entry.Name })
	return out
}
