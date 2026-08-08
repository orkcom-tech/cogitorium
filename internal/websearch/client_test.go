package websearch

import (
	"testing"
)

// A proxy would make every address check inspect the proxy instead of the
// real destination: the control would keep passing its own tests while
// guarding nothing. Refusing to start is the only honest response.
func TestRefusesToStartUnderAProxy(t *testing.T) {
	for _, v := range proxyVars {
		t.Run(v, func(t *testing.T) {
			t.Setenv(v, "http://127.0.0.1:3128")
			if _, err := New("127.0.0.1:8688", "a-key"); err == nil {
				t.Fatalf("the searcher started with %s set", v)
			}
		})
	}
}

func TestRefusesToStartWithoutACredential(t *testing.T) {
	if _, err := New("127.0.0.1:8688", "   "); err == nil {
		t.Fatal("the searcher started with no credential; it would send unauthenticated requests")
	}
}

// Reflection-free assertions on the constructed client: these are the
// settings whose quiet loss would not fail any other test.
func TestClientNeverFollowsRedirectsAndNeverUsesAProxy(t *testing.T) {
	s, err := New("127.0.0.1:8688", "a-key")
	if err != nil {
		t.Fatalf("construction failed: %v", err)
	}
	if s.client.CheckRedirect == nil {
		t.Error("redirects would be followed, moving the destination after the operator approved it")
	}
	tr, ok := s.client.Transport.(guard)
	if !ok {
		t.Fatalf("the per-request authority guard is not installed: %T", s.client.Transport)
	}
	_ = tr
	if s.client.Timeout == 0 {
		t.Error("no client timeout: a fetch could outlive the server's shutdown grace")
	}
}
