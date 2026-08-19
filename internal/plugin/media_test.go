package plugin

import "testing"

// A picture is the author's choice, and a plugin that ships none must not be
// worse off for it — not refused, not warned at, not marked incomplete.
//
// This is the test rather than a line in a comment because "optional" is the
// kind of property that survives review and dies in a required-fields list.
func TestMediaIsOptional(t *testing.T) {
	m := Manifest{
		Schema:  1,
		ID:      "quiet",
		Name:    "Quiet",
		Version: "1.0.0",
		Host:    Host{Contract: 1},
	}
	if problems := m.Validate(); len(problems) > 0 {
		t.Fatalf("a plugin that ships no media was refused: %v", problems)
	}

	// And the field an author who wants one fills in.
	m.Media = []Medium{{File: "docs/screen.png", Caption: "The list"}}
	if problems := m.Validate(); len(problems) > 0 {
		t.Fatalf("a plugin that ships one was refused: %v", problems)
	}
}

// What a medium may be is a closed list, because this field is how a bundle
// gets a browser to open a file nobody has looked at.
func TestMediaIsOnlyWhatPlaysWithoutScript(t *testing.T) {
	for _, file := range []string{"a.png", "a.jpg", "a.webp", "a.gif", "a.avif", "a.mp4", "a.webm"} {
		if MediaKind(file) == "" {
			t.Errorf("%s is not shown, and it should be", file)
		}
	}
	for _, file := range []string{"a.svg", "a.html", "a.pdf", "a.js", "a.exe", "a"} {
		if MediaKind(file) != "" {
			t.Errorf("%s is shown, and it should not be", file)
		}
	}

	m := Manifest{
		Schema: 1, ID: "loud", Name: "Loud", Version: "1.0.0", Host: Host{Contract: 1},
		Media: []Medium{{File: "docs/thing.svg"}},
	}
	if problems := m.Validate(); len(problems) == 0 {
		t.Error("an svg was accepted as media; it is a document that can carry script")
	}
}

// A cover on somebody else's host would tell them who is browsing the library,
// before anybody has installed anything.
func TestACoverStaysOnTheAuthorsOwnRepository(t *testing.T) {
	ok := Entry{
		ID: "release-radar", Name: "Release Radar", Author: "someone",
		Description: "watches releases", Repo: "someone/cogitorium-release-radar",
		Cover: "https://raw.githubusercontent.com/someone/cogitorium-release-radar/main/docs/cover.png",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a cover in the author's own repository was refused: %v", err)
	}

	away := ok
	away.Cover = "https://tracker.example.com/pixel.png"
	if err := away.Validate(); err == nil {
		t.Error("a cover on another host was accepted")
	}

	none := ok
	none.Cover = ""
	if err := none.Validate(); err != nil {
		t.Fatalf("an entry with no cover was refused: %v", err)
	}
}
