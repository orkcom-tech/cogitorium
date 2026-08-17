package gear

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The browser environment, in a real container with a real browser in it.
//
// The claim is small and worth stating exactly: an operator can grant a gear a
// machine that has a browser, and what the browser produces comes back as
// ordinary run artifacts — because out/ already carries files and the record
// already knows how to hold them. Nothing here is a new pipeline.
//
// What makes it worth a test rather than a paragraph is the constraints. A gear
// runs as an unprivileged user with every capability dropped and no new
// privileges, which is exactly the situation a browser's own sandbox cannot
// start in. That it renders at all under those flags is a fact about this
// arrangement, not something readable from the code.
const browserGear = `set -e
CHROME=$(ls -d /ms-playwright/chromium-*/chrome-linux/chrome 2>/dev/null | head -1)
if [ -z "$CHROME" ]; then
  echo "NO-BROWSER" >&2
  exit 1
fi
echo "BROWSER=$CHROME"
mkdir -p out
cat > page.html <<'PAGE'
<html><head><title>a page</title></head><body><h1>rendered by a real browser</h1></body></html>
PAGE
"$CHROME" --headless --no-sandbox --disable-gpu \
  --screenshot=out/shot.png --window-size=800,400 file://$PWD/page.html 2>/dev/null
"$CHROME" --headless --no-sandbox --dump-dom file://$PWD/page.html 2>/dev/null > out/page.txt
echo "SHOT=$(wc -c < out/shot.png) TEXT=$(wc -c < out/page.txt)"
`

func TestAGearGrantedTheBrowserEnvironmentGetsOneAndItsOutputComesBack(t *testing.T) {
	s := newSandboxed(t)
	ctx := context.Background()

	image := browserImageOrSkip(t)
	s.exec.SetBrowserImage(image)

	g := s.approveScript(t, "shoot", browserGear)

	// Ungranted first, and the point is which IMAGE the run got rather than what
	// the gear then managed to do in it. This install's ordinary sandbox image
	// carries no browser — here it carries no shell either — so the run cannot
	// report one. That is the grant doing its work: without it, the gear is
	// somewhere else entirely.
	res, err := s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err == nil && strings.Contains(res.Stdout, "BROWSER=") {
		t.Fatalf("a gear nobody granted a browser found one: %s", res.Stdout)
	}
	t.Logf("ungranted, the run reported: %v / %s", err, strings.TrimSpace(res.Stderr))

	if g, err = s.gears.SetEnvironment(ctx, g.ID, EnvironmentBrowser); err != nil {
		t.Fatalf("grant the browser environment: %v", err)
	}
	res, err = s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run a gear granted a browser: %v (stderr: %s)", err, res.Stderr)
	}
	t.Logf("the gear said: %s", strings.TrimSpace(res.Stdout))
	if !strings.Contains(res.Stdout, "BROWSER=/ms-playwright/") {
		t.Fatalf("the run did not get the browser image: %s", res.Stdout)
	}
	// A screenshot that is a few bytes is a browser that started and rendered
	// nothing, which would pass a "does the file exist" check.
	if !strings.Contains(res.Stdout, "SHOT=") || strings.Contains(res.Stdout, "SHOT=0") {
		t.Fatalf("no screenshot came back: %s", res.Stdout)
	}
}

// An environment this install does not have is refused when it is set, not
// when a gear next runs. The operator is at the screen at one of those moments.
func TestAnUnknownEnvironmentIsRefusedAtTheMomentItIsSet(t *testing.T) {
	f := newFixture(t)
	g, err := f.gears.Forge(context.Background(), "any", "", nil, "bash", "main.sh", "", nil,
		[]File{{Path: "main.sh", Content: "echo hi"}}, f.wsID, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.gears.SetEnvironment(context.Background(), g.ID, "chrome"); err == nil {
		t.Fatal("an environment this install has never heard of was accepted, so the gear would " +
			"silently run in the ordinary image and its author would go looking at their own code")
	} else if !strings.Contains(err.Error(), "browser") {
		t.Fatalf("the refusal does not say what is available: %v", err)
	}
	// And the ordinary one is settable, which is how a grant is taken back.
	if got, err := f.gears.SetEnvironment(context.Background(), g.ID, EnvironmentDefault); err != nil {
		t.Fatalf("clearing the environment: %v", err)
	} else if got.Environment != "" {
		t.Fatalf("the environment did not clear: %q", got.Environment)
	}
}

// Forging a new version clears the environment along with the approval.
// Approval covers exact content, and so does everything granted in the same
// act — otherwise an agent could rewrite a gear's code and inherit a browser.
func TestForgingANewVersionTakesTheBrowserBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := f.gears.Forge(ctx, "shoot", "", nil, "bash", "main.sh", "", nil,
		[]File{{Path: "main.sh", Content: "echo one"}}, f.wsID, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.gears.SetEnvironment(ctx, g.ID, EnvironmentBrowser); err != nil {
		t.Fatal(err)
	}
	again, err := f.gears.Forge(ctx, "shoot", "", nil, "bash", "main.sh", "", nil,
		[]File{{Path: "main.sh", Content: "echo two"}}, f.wsID, f.agentID)
	if err != nil {
		t.Fatalf("forge a second version: %v", err)
	}
	if again.Environment != "" {
		t.Fatal("a new version inherited the browser, so an agent could rewrite the code under a " +
			"capability an operator granted to different code")
	}
}

// approveScript forges and approves a shell gear, which is what a browser gear
// is in practice: a few lines driving a binary the image already has.
func (s *sandboxed) approveScript(t *testing.T, name, script string) Gear {
	t.Helper()
	ctx := context.Background()
	g, err := s.gears.Forge(ctx, name, "a shell test gear", nil, "bash", "main.sh", "", nil,
		[]File{{Path: "main.sh", Content: script}}, s.wsID, s.agentID)
	if err != nil {
		t.Fatalf("forge %q: %v", name, err)
	}
	if g, err = s.gears.SetStatus(ctx, g.ID, StatusApproved, Actor{Name: "test-operator"}); err != nil {
		t.Fatalf("approve %q: %v", name, err)
	}
	return g
}

// browserImageOrSkip names an image on this machine that carries a browser.
//
// Not pulled here: it is a gigabyte, and a test that downloads one is a test
// that fails on a slow connection for a reason that has nothing to do with the
// code. Absent, this skips and says what to run.
func browserImageOrSkip(t *testing.T) string {
	t.Helper()
	image := os.Getenv("COGITORIUM_TEST_BROWSER_IMAGE")
	if image == "" {
		image = defaultTestBrowserImage
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("%s is not on this machine; docker pull %s to run this test", image, image)
	}
	return image
}

// The same image the shipping default names, so what this test proves is what
// an install gets rather than something adjacent to it.
const defaultTestBrowserImage = "mcr.microsoft.com/playwright:v1.56.0-noble"
