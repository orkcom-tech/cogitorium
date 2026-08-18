package settings

import (
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open a database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

// A fresh install has answered nothing, and that is not a failure. A caller
// forced to tell sql.ErrNoRows from a real fault would eventually read a broken
// database as an unanswered question.
func TestNothingStoredIsNotAnError(t *testing.T) {
	s := newStore(t)
	v, err := s.Get(t.Context(), KeyUpdateCheck)
	if err != nil {
		t.Fatalf("reading an unset key was an error: %v", err)
	}
	if v != "" {
		t.Fatalf("an unset key came back %q", v)
	}
}

func TestAnAnswerIsReadBack(t *testing.T) {
	s := newStore(t)
	if err := s.Set(t.Context(), KeyUpdateCheck, "on"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err := s.Get(t.Context(), KeyUpdateCheck)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "on" {
		t.Fatalf("stored `on`, read back %q", v)
	}
}

// Answering again replaces the answer rather than failing on the primary key
// or leaving two rows for one question.
func TestAnsweringAgainReplacesTheAnswer(t *testing.T) {
	s := newStore(t)
	for _, v := range []string{"on", "off", "ask", "off"} {
		if err := s.Set(t.Context(), KeyUpdateCheck, v); err != nil {
			t.Fatalf("set %q: %v", v, err)
		}
	}
	got, err := s.Get(t.Context(), KeyUpdateCheck)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "off" {
		t.Fatalf("the last answer was `off`, read back %q", got)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	s := newStore(t)
	if err := s.Set(t.Context(), KeyUpdateCheck, "on"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err := s.Get(t.Context(), "something-else")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "" {
		t.Fatalf("an unrelated key came back %q", v)
	}
}
