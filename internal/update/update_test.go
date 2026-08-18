package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The subject of these tests is not "does it parse JSON". It is whether this
// package can be made to send a request nobody agreed to, and whether it can
// tell an operator something it does not actually know.

// release serves one GitHub-shaped answer per repository.
func release(t *testing.T, byRepo map[string]string) *Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for repo, body := range byRepo {
			if r.URL.Path == "/repos/"+repo+"/releases/latest" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c := New(ModeOn, "v1.5.0", func(context.Context) string { return "" })
	c.base = srv.URL
	return c
}

func body(tag string) string {
	return `{"tag_name":"` + tag + `","name":"` + tag + `","body":"notes","html_url":"https://example.invalid/r","published_at":"2026-08-18T00:00:00Z"}`
}

func TestOffMakesNoRequestEvenWhenAskedDirectly(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	t.Cleanup(srv.Close)

	c := New(ModeOff, "v1.5.0", nil)
	c.base = srv.URL

	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("a check ran with update_check off; that setting is the one promise this package makes")
	}
	if reached {
		t.Fatal("update_check is off and a request still went out")
	}
}

// The configured off is a decision made on the server's own disk. A browser
// must not be able to undo it, or the file is a suggestion.
func TestOffCannotBeTurnedOnFromTheInterface(t *testing.T) {
	c := New(ModeOff, "v1.5.0", nil)
	if err := c.SetMode(context.Background(), ModeOn); err == nil {
		t.Fatal("the interface switched on a check the configuration switched off")
	}
	if c.Mode() != ModeOff {
		t.Fatalf("mode is %q after a refused change", c.Mode())
	}
}

func TestAskMakesNoRequestUntilSomebodyAnswers(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body("v1.6.0")))
	}))
	t.Cleanup(srv.Close)

	c := New(ModeAsk, "v1.5.0", nil)
	c.base = srv.URL

	// Start is the timer, and under ask there is no timer at all.
	c.Start(context.Background())
	if reached {
		t.Fatal("the background check ran while the setting was still `ask`")
	}
	// Reading the report is what a screen opening does, and it must not be a
	// packet on the wire.
	if r := c.Report(); r.Mode != ModeAsk || r.CheckedAt.IsZero() == false {
		t.Fatalf("reading the report under ask produced %+v", r)
	}
	if reached {
		t.Fatal("reading the report sent a request")
	}
}

// A deliberate press under `ask` is an answer for that one look and nothing
// more: it must not quietly become a standing daily request.
func TestCheckNowUnderAskDoesNotBecomeAStandingGrant(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	c.mode = ModeAsk

	if _, err := c.Check(context.Background()); err != nil {
		t.Fatalf("check now was refused under ask: %v", err)
	}
	if c.Mode() != ModeAsk {
		t.Fatalf("one press changed the setting to %q", c.Mode())
	}
}

func TestANewerReleaseIsReported(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	r, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	cog := find(t, r, ProductCogitorium)
	if !cog.Newer {
		t.Fatalf("v1.6.0 against v1.5.0 did not read as newer: %+v", cog)
	}
	if !r.Any() {
		t.Fatal("the report says nothing has moved on while one of its products has")
	}
}

func TestTheSameVersionIsNotAnUpdate(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.5.0")})
	r, _ := c.Check(context.Background())
	cog := find(t, r, ProductCogitorium)
	if cog.Newer {
		t.Fatal("the running version was offered to itself as an update")
	}
	if !cog.Comparable {
		t.Fatal("two release versions did not compare")
	}
}

// A development build knows what it is. Telling somebody who built this
// themselves that a tag exists is a notice they cannot act on.
func TestADevelopmentBuildIsNeverToldToUpdate(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	c.running = "0.0.0-dev"

	r, _ := c.Check(context.Background())
	cog := find(t, r, ProductCogitorium)
	if cog.Newer {
		t.Fatal("a dev build was told to update")
	}
	if cog.Comparable {
		t.Fatal("a dev build reported a conclusive comparison; `up to date` and `cannot say` are different answers")
	}
	if cog.Latest == nil {
		t.Fatal("the release was not reported at all — a dev build may still look")
	}
}

// Contextverse not being installed is an ordinary state, not an error, and
// certainly not a reason to advertise it.
func TestAMissingContextverseIsNotAnUpdateNotice(t *testing.T) {
	c := release(t, map[string]string{
		repoCogitorium:   body("v1.5.0"),
		repoContextverse: body("v1.0.0"),
	})
	r, _ := c.Check(context.Background())
	cv := find(t, r, ProductContextverse)
	if cv.Latest != nil || cv.Newer || cv.Error != "" {
		t.Fatalf("a Contextverse that is not installed produced %+v", cv)
	}
}

func TestAnInstalledContextverseIsCheckedToo(t *testing.T) {
	c := release(t, map[string]string{
		repoCogitorium:   body("v1.5.0"),
		repoContextverse: body("v1.1.0"),
	})
	c.contextd = func(context.Context) string { return "1.0.0" }

	r, _ := c.Check(context.Background())
	cv := find(t, r, ProductContextverse)
	if !cv.Newer {
		t.Fatalf("an older contextd was not told about a newer one: %+v", cv)
	}
}

// Half an answer is worth having: one product being unreachable must not take
// the other one's answer with it.
func TestOneHalfFailingDoesNotSilenceTheOther(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	c.contextd = func(context.Context) string { return "1.0.0" }

	r, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !find(t, r, ProductCogitorium).Newer {
		t.Fatal("Cogitorium's answer was lost because Contextverse's failed")
	}
	if find(t, r, ProductContextverse).Error == "" {
		t.Fatal("a failed half reported no reason; an operator cannot tell `current` from `could not ask`")
	}
}

func TestAPrereleaseIsNeverOffered(t *testing.T) {
	c := release(t, map[string]string{
		repoCogitorium: `{"tag_name":"v2.0.0","prerelease":true,"body":"","html_url":"","published_at":""}`,
	})
	r, _ := c.Check(context.Background())
	cog := find(t, r, ProductCogitorium)
	if cog.Latest != nil || cog.Newer {
		t.Fatalf("a prerelease was offered as an update: %+v", cog)
	}
}

func TestReleaseNotesAreBounded(t *testing.T) {
	huge := strings.Repeat("x", notesCap*3)
	c := release(t, map[string]string{
		repoCogitorium: `{"tag_name":"v1.6.0","body":"` + huge + `","html_url":"","published_at":""}`,
	})
	r, _ := c.Check(context.Background())
	cog := find(t, r, ProductCogitorium)
	if cog.Latest == nil {
		t.Fatal("no release")
	}
	if len(cog.Latest.Notes) > notesCap+200 {
		t.Fatalf("release notes came back %d bytes; somebody else's text is rendered in this interface", len(cog.Latest.Notes))
	}
}

func TestVersionsThatCannotBeComparedSaySo(t *testing.T) {
	for _, s := range []string{"", "dev", "1.5", "1.5.0.1", "v1.6.0-rc1", "0.0.0-dev", "v1.x.0"} {
		if _, ok := parse(s); ok {
			t.Fatalf("%q parsed as a release version; it is not one", s)
		}
	}
	for _, s := range []string{"v1.5.0", "1.5.0", "v0.0.1", "v12.30.400"} {
		if _, ok := parse(s); !ok {
			t.Fatalf("%q is a release version and did not parse", s)
		}
	}
}

func TestOrderingIsByFieldNotByString(t *testing.T) {
	// The one every hand-rolled comparison gets wrong: "10" sorts before "9"
	// as a string and after it as a number.
	older, _ := parse("v1.9.0")
	newer, _ := parse("v1.10.0")
	if !newer.after(older) {
		t.Fatal("1.10.0 did not read as newer than 1.9.0")
	}
	if older.after(newer) {
		t.Fatal("1.9.0 read as newer than 1.10.0")
	}
	if newer.after(newer) {
		t.Fatal("a version reads as newer than itself")
	}
}

// Switching the check off must not leave yesterday's finding on screen: that
// is the product ignoring the answer it asked for.
func TestTurningTheCheckOffClearsWhatItFound(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !c.Report().Any() {
		t.Fatal("nothing was found to clear")
	}
	if err := c.SetMode(context.Background(), ModeAsk); err != nil {
		t.Fatalf("set ask: %v", err)
	}
	if c.Report().Any() {
		t.Fatal("a finding survived the check being switched off")
	}
}

func TestAnUnknownModeIsTreatedAsOff(t *testing.T) {
	c := New("sometimes", "v1.5.0", nil)
	if c.Mode() != ModeOff {
		t.Fatalf("an unparseable setting became %q; the safe reading is the one that makes no requests", c.Mode())
	}
}

// Install never writes anything, so the only thing it can get wrong is the
// sentence — but a sentence offering `brew upgrade` to somebody who has no brew
// is the sentence that wastes an afternoon.
func TestInstallAlwaysAnswersSomething(t *testing.T) {
	got := Install()
	if got.Kind == "" {
		t.Fatal("Install named no kind; `unknown` is an answer and empty is not")
	}
	if got.Note == "" {
		t.Fatalf("Install kind %q carries no sentence for the operator", got.Kind)
	}
	// A container and a cluster have no honest command, and offering one there
	// is worse than offering none.
	if (got.Kind == "container" || got.Kind == "kubernetes") && got.Command != "" {
		t.Fatalf("kind %q offered the command %q, which the next deploy would undo", got.Kind, got.Command)
	}
}

func find(t *testing.T, r Report, name string) Product {
	t.Helper()
	for _, p := range r.Products {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no %s in the report", name)
	return Product{}
}

// ── the two bugs a review caught, kept caught ────────────────────────────────

// remembers is a Settings that lives in a map, so the persistence rules can be
// tested without a database.
type remembers struct {
	v      map[string]string
	setErr error
	getErr error
}

func newRemembers() *remembers { return &remembers{v: map[string]string{}} }

func (r *remembers) Get(_ context.Context, k string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	return r.v[k], nil
}

func (r *remembers) Set(_ context.Context, k, val string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.v[k] = val
	return nil
}

// THE FIRST BUG. Start runs once, at construction, when the setting is usually
// still `ask` — so answering `on` afterwards used to give exactly one check and
// then silence until the process was restarted.
func TestAnsweringOnStartsTheTimerThatAskNeverHad(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	c.mode = ModeAsk
	c.ceiling = ModeAsk
	c.Start(context.Background())

	if c.tickers() != 0 {
		t.Fatal("a timer was running while the setting was still `ask`")
	}
	if err := c.SetMode(context.Background(), ModeOn); err != nil {
		t.Fatalf("set on: %v", err)
	}
	if c.tickers() != 1 {
		t.Fatal("answering `on` did not start the timer: the operator gets one check and then silence")
	}
}

// And answering twice must not leave two goroutines asking on two different
// days.
func TestTheTimerIsNotStartedTwice(t *testing.T) {
	c := release(t, map[string]string{repoCogitorium: body("v1.6.0")})
	c.mode = ModeAsk
	c.ceiling = ModeAsk
	c.Start(context.Background())

	if err := c.SetMode(context.Background(), ModeOn); err != nil {
		t.Fatalf("set on: %v", err)
	}
	c.tick(0)
	c.tick(0)
	if n := c.tickers(); n != 1 {
		t.Fatalf("%d timers are running; one operator answering once must produce one", n)
	}
}

// THE SECOND BUG. The answer used to live only in memory, so the product asked
// the same question after every restart and looked like it had not listened.
func TestTheAnswerSurvivesARestart(t *testing.T) {
	mem := newRemembers()

	first := New(ModeAsk, "v1.5.0", nil)
	first.Load(context.Background(), mem)
	if err := first.SetMode(context.Background(), ModeOff); err != nil {
		t.Fatalf("set off: %v", err)
	}

	// A new process, same configuration, same store.
	second := New(ModeAsk, "v1.5.0", nil)
	second.Load(context.Background(), mem)
	if second.Mode() != ModeOff {
		t.Fatalf("after a restart the setting is %q; the operator already answered off", second.Mode())
	}
}

// The config file is the ceiling. A row written before somebody edited that
// file must not outrank it.
func TestAStoredAnswerCannotOutrankAConfiguredOff(t *testing.T) {
	mem := newRemembers()
	mem.v[SettingKey] = ModeOn

	c := New(ModeOff, "v1.5.0", nil)
	c.Load(context.Background(), mem)
	if c.Mode() != ModeOff {
		t.Fatalf("a stored `on` lifted a configured `off`: mode is %q", c.Mode())
	}
}

// A store that cannot be read leaves the configured value standing rather than
// leaving the check in some unknown state.
func TestAnUnreadableStoreFallsBackToTheConfiguredSetting(t *testing.T) {
	mem := newRemembers()
	mem.getErr = errors.New("disk gone")

	c := New(ModeAsk, "v1.5.0", nil)
	c.Load(context.Background(), mem)
	if c.Mode() != ModeAsk {
		t.Fatalf("mode is %q after an unreadable store; want the configured ask", c.Mode())
	}
}

// A failed write must not fail the operator's click: the setting holds for this
// process, and they are told it will be asked again.
func TestAFailedWriteStillChangesTheSettingNow(t *testing.T) {
	mem := newRemembers()
	mem.setErr = errors.New("read-only database")

	c := New(ModeAsk, "v1.5.0", nil)
	c.Load(context.Background(), mem)
	if err := c.SetMode(context.Background(), ModeOff); err != nil {
		t.Fatalf("a failed write failed the whole call: %v", err)
	}
	if c.Mode() != ModeOff {
		t.Fatalf("mode is %q; the answer should hold for this process even unstored", c.Mode())
	}
}

// Nonsense in the store is ignored rather than adopted.
func TestAnUnparseableStoredAnswerIsIgnored(t *testing.T) {
	mem := newRemembers()
	mem.v[SettingKey] = "whenever"

	c := New(ModeAsk, "v1.5.0", nil)
	c.Load(context.Background(), mem)
	if c.Mode() != ModeAsk {
		t.Fatalf("mode is %q after nonsense in the store; want the configured ask", c.Mode())
	}
}
