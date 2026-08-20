package view

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// This product renders half its screens as documents and half in the browser,
// and each half draws its own rail. That is a fact about a conversion in
// progress, not a design — and it means every difference between the two shows
// up to somebody as "why does this screen behave differently".
//
// The two renderers stay. What must not differ is WHAT IS IN THE RAIL, so the
// lists are checked against each other here: same destinations, same order.
func TestTheRailsCarryTheSameDestinations(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "Rail.tsx"))
	if err != nil {
		t.Skipf("the frontend source is not present: %v", err)
	}
	block := regexp.MustCompile(`(?s)const DESTINATIONS[^\[]*\[(.*?)\n\]`).FindStringSubmatch(string(b))
	if block == nil {
		t.Fatal("DESTINATIONS is no longer a list in Rail.tsx — this test reads it, so it has to be found")
	}
	client := regexp.MustCompile(`href:\s*"([^"]+)"`).FindAllStringSubmatch(block[1], -1)

	// The host's own rail, as an administrator sees it: the client filters the
	// admin-only entries in the browser, so both lists are compared whole.
	var server []string
	for _, item := range HostNav("", true) {
		// Account is the viewer's own and is drawn differently on each side.
		if item.Href == "/account" {
			continue
		}
		server = append(server, item.Href)
	}
	if len(client) != len(server) {
		t.Fatalf("the application offers %d destinations and this server offers %d", len(client), len(server))
	}
	for i := range server {
		if client[i][1] != server[i] {
			t.Errorf("destination %d: the application goes to %s, this server goes to %s", i+1, client[i][1], server[i])
		}
	}
}
