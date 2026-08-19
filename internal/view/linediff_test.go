package view

import "testing"

func renderDiff(lines []DiffLine) string {
	out := ""
	for _, l := range lines {
		switch l.Kind {
		case "added":
			out += "+" + l.Text + "\n"
		case "removed":
			out += "-" + l.Text + "\n"
		default:
			out += " " + l.Text + "\n"
		}
	}
	return out
}

func TestADiffShowsOnlyWhatMoved(t *testing.T) {
	got := renderDiff(mustDiff(t,
		"import json\nprint(1)\ndone()\n",
		"import json\nprint(2)\ndone()\n"))
	want := " import json\n-print(1)\n+print(2)\n done()\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnInsertionIsNotReportedAsARewrite(t *testing.T) {
	// The failure a naive line-by-line comparison makes: one inserted line
	// and everything after it reads as changed, which buries the one line
	// somebody needs to see.
	got := renderDiff(mustDiff(t, "a\nb\nc\n", "a\nnew\nb\nc\n"))
	want := " a\n+new\n b\n c\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnEmptyBeforeIsAllAdded(t *testing.T) {
	got := renderDiff(mustDiff(t, "", "a\nb\n"))
	if got != "+a\n+b\n" {
		t.Fatalf("got:\n%s", got)
	}
}

// A trailing newline is a line ending, not an empty last line. Treating it as
// one reports a change every time somebody's editor adds or removes it.
func TestATrailingNewlineIsNotALine(t *testing.T) {
	lines, ok := DiffLines("a\n", "a")
	if !ok {
		t.Fatal("refused a two-line comparison")
	}
	for _, l := range lines {
		if l.Kind != "same" {
			t.Fatalf("a trailing newline read as a change: %+v", lines)
		}
	}
}

func TestAPairTooLargeIsRefusedRatherThanCompared(t *testing.T) {
	big := ""
	for i := 0; i < maxDiffLines; i++ {
		big += "line\n"
	}
	if _, ok := DiffLines(big, big); ok {
		t.Fatal("a pair past the ceiling was compared anyway")
	}
}

func mustDiff(t *testing.T, before, after string) []DiffLine {
	t.Helper()
	lines, ok := DiffLines(before, after)
	if !ok {
		t.Fatal("the comparison was refused")
	}
	return lines
}
