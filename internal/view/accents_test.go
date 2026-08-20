package view

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The eight colours are stated twice — once for the application's own menu and
// once for the server's — so they are checked against each other rather than
// trusted. A product that offered different colours on half its screens would
// be two products, and the drift would be invisible until somebody chose one
// of the missing ones.
func TestTheAccentsMatchTheApplication(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "styles", "theme.ts"))
	if err != nil {
		t.Skipf("the frontend source is not present: %v", err)
	}
	block := regexp.MustCompile(`(?s)export const ACCENTS[^\[]*\[(.*?)\n\]`).FindStringSubmatch(string(b))
	if block == nil {
		t.Fatal("ACCENTS is no longer a list in theme.ts — this test reads it, so it has to be found")
	}
	pairs := regexp.MustCompile(`\{\s*name:\s*'([^']+)',\s*hex:\s*'([^']+)'\s*\}`).FindAllStringSubmatch(block[1], -1)
	if len(pairs) != len(Accents) {
		t.Fatalf("the application offers %d colours and this server offers %d", len(pairs), len(Accents))
	}
	for i, p := range pairs {
		if p[1] != Accents[i].Name || !strings.EqualFold(p[2], Accents[i].Hex) {
			t.Errorf("colour %d: the application says %s %s, this server says %s %s",
				i+1, p[1], p[2], Accents[i].Name, Accents[i].Hex)
		}
	}
}
