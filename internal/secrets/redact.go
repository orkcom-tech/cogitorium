package secrets

import (
	"sort"
	"strings"
	"sync"
)

// Placeholder is what a secret's value is replaced with. It is deliberately
// visible: an operator reading a gear's output should be able to see that
// something was removed, rather than wonder why a line is malformed.
const Placeholder = "[redacted]"

// maxHold bounds how much live output a Stream may sit on while waiting to
// decide. A secret longer than this, split across two chunks, reaches the
// watching operator's screen unredacted — but never the recorded run, the
// agent's context or the log, all of which are redacted from the whole buffered
// output. Losing that bound instead would mean a gear with a large secret shows
// nothing at all until it exits, which defeats live output entirely.
const maxHold = 4096

// Redactor removes one run's secret values from anything that could be read
// afterwards.
//
// It is built from the values ACTUALLY INJECTED into that run and no others.
// Scanning every secret in the install would be slower, and worse: a gear could
// then learn which secrets exist by printing guesses and seeing which came back
// redacted.
type Redactor struct {
	values []string
	maxLen int
}

// NewRedactor takes the plaintext values to remove. Empty ones are dropped —
// an empty needle would match everywhere and turn all output into placeholders
// — and the store refuses to hold one in the first place.
func NewRedactor(values ...string) *Redactor {
	r := &Redactor{}
	for _, v := range values {
		if v == "" {
			continue
		}
		r.values = append(r.values, v)
		if len(v) > r.maxLen {
			r.maxLen = len(v)
		}
	}
	// Longest first, so a secret that contains another is replaced whole
	// instead of leaving the difference behind for anyone to read.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

// Active reports whether this redactor has anything to do. A run that was given
// only variables gets no redaction and no cost.
func (r *Redactor) Active() bool { return r != nil && len(r.values) > 0 }

// String replaces every occurrence of every value.
func (r *Redactor) String(s string) string {
	if !r.Active() || s == "" {
		return s
	}
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, Placeholder)
	}
	return s
}

// Strings redacts a slice in place-equivalent form, for the lists of file names
// a run reports back.
func (r *Redactor) Strings(in []string) []string {
	if !r.Active() || len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = r.String(s)
	}
	return out
}

// Error redacts an error's message while keeping it unwrappable, so errors.Is
// still works on whatever it wrapped. Nothing in this codebase prints an
// unwrapped cause, and the alternative — a flat errors.New — would break the
// sentinel checks the callers rely on.
func (r *Redactor) Error(err error) error {
	if err == nil || !r.Active() {
		return err
	}
	msg := r.String(err.Error())
	if msg == err.Error() {
		return err
	}
	return redacted{msg: msg, err: err}
}

type redacted struct {
	msg string
	err error
}

func (e redacted) Error() string { return e.msg }
func (e redacted) Unwrap() error { return e.err }

// Tap wraps a live-output sink so a secret cannot reach a watching operator
// ahead of the redacted record, and returns the flush that must follow the run.
//
// The flush is not optional: a chunk's tail is held back until enough follows
// it to decide (see Stream), so without it the last few bytes of a gear's
// output would simply never arrive.
func (r *Redactor) Tap(on func(stream, chunk string)) (func(stream, chunk string), func()) {
	if on == nil || !r.Active() {
		return on, func() {}
	}
	var mu sync.Mutex
	streams := map[string]*Stream{}
	get := func(name string) *Stream {
		s, ok := streams[name]
		if !ok {
			s = r.Stream()
			streams[name] = s
		}
		return s
	}
	wrapped := func(stream, chunk string) {
		mu.Lock()
		out := get(stream).Chunk(chunk)
		mu.Unlock()
		if out != "" {
			on(stream, out)
		}
	}
	flush := func() {
		mu.Lock()
		names := make([]string, 0, len(streams))
		for name := range streams {
			names = append(names, name)
		}
		sort.Strings(names)
		tails := make([]string, 0, len(names))
		for _, name := range names {
			tails = append(tails, streams[name].Flush())
		}
		mu.Unlock()
		for i, tail := range tails {
			if tail != "" {
				on(names[i], tail)
			}
		}
	}
	return wrapped, flush
}

// Stream redacts a value that arrives in pieces.
//
// A secret split across two chunks — "sk-live" then "-abc123" — matches
// neither piece, so the tail of each chunk is held back until either enough has
// followed it to decide or the stream ends. Chunks are whatever the pipe
// delivered; a gear printing a progress bar without newlines is exactly the one
// worth watching, so there is no line buffering to lean on.
type Stream struct {
	r     *Redactor
	carry string
}

func (r *Redactor) Stream() *Stream { return &Stream{r: r} }

func (s *Stream) Chunk(text string) string {
	if !s.r.Active() {
		return text
	}
	buf := s.r.String(s.carry + text)
	hold := min(s.r.maxLen-1, maxHold)
	if hold > len(buf) {
		hold = len(buf)
	}
	s.carry = buf[len(buf)-hold:]
	return buf[:len(buf)-hold]
}

// Flush releases the held-back tail once nothing more can arrive.
func (s *Stream) Flush() string {
	out := s.r.String(s.carry)
	s.carry = ""
	return out
}
