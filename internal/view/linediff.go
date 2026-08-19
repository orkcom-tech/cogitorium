package view

// A line diff, for showing what changed in a gear between versions.
//
// An approval covers exact content, so the question at review time is not
// "what is this code" but "what is different from the version I already read".
// The application computes this in the browser; a server-rendered screen has
// to compute it here or stop offering it, and stopping would take a review aid
// away from the operator to save this package thirty lines.
//
// Longest common subsequence, on whole lines. Not Myers: a gear is a handful
// of files somebody is about to read, the quadratic term is bounded by a
// ceiling below, and an algorithm nobody in this repository can follow is a
// worse trade than a few milliseconds on a page that is already fetching
// source out of SQLite.

// DiffLine is one line of a comparison.
type DiffLine struct {
	// Kind is "same", "added" or "removed" — the word a template switches on,
	// so a plugin overriding the row does not have to know three constants.
	Kind string
	Text string
}

// maxDiffLines bounds the comparison. Past it the pair is reported as
// incomparable rather than compared slowly: a file that large is not being
// read line by line at review time anyway, and a page that hangs is worse than
// one that says it will not do this.
const maxDiffLines = 4000

// DiffLines compares two versions of a file.
//
// The second result is false when the pair is too large to compare, so the
// caller says so rather than showing an empty diff that reads as "nothing
// changed".
func DiffLines(before, after string) ([]DiffLine, bool) {
	a, b := splitLines(before), splitLines(after)
	if len(a)+len(b) > maxDiffLines {
		return nil, false
	}

	// lcs[i][j] is the length of the longest common subsequence of a[i:] and
	// b[j:]. Built backwards so the walk forwards below reads in file order,
	// which is the order somebody reads the result in.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{Kind: "same", Text: a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{Kind: "removed", Text: a[i]})
			i++
		default:
			out = append(out, DiffLine{Kind: "added", Text: b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, DiffLine{Kind: "removed", Text: a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, DiffLine{Kind: "added", Text: b[j]})
	}
	return out, true
}

// splitLines keeps the file's own shape: a trailing newline is a line ending
// rather than an empty last line, and treating it as one would report a change
// every time somebody's editor adds or removes it.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
