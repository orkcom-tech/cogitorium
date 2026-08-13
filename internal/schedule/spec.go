// Package schedule works out when something should next happen.
//
// Two forms and no more. `every 15m` covers most of what anybody wants, and a
// five-field cron subset covers the rest — "07:00 on weekdays" is the shape of
// request that a duration cannot express and that people already know how to
// write. A third form would be a third parser to be wrong in a new way.
//
// Deliberately no seconds field, no @yearly, no L/W/#, no step on the day of
// week beyond the plain forms below. Every one of those exists in some cron
// somewhere, and each is a thing an operator can write, believe, and be wrong
// about. What is refused here is refused when the schedule is SAVED, with a
// sentence saying what may be written instead — which is the only moment the
// person is still looking at what they typed.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	// The database carries a timezone name, and a machine that has no zoneinfo
	// installed would otherwise resolve every one of them to UTC — quietly, and
	// only in production. Embedding it costs about 450 KiB and removes an
	// entire class of "it fired an hour late on the server".
	_ "time/tzdata"
)

// MinEvery is the floor for the duration form.
//
// Not arbitrary: a schedule firing faster than this cannot finish before it is
// due again, because a run holds its workspace's lane for the whole of a model
// call. Anything below it would build a backlog by construction and look like
// the product being slow.
const MinEvery = time.Minute

// Spec is a parsed schedule. The zero value is not usable; use Parse.
type Spec struct {
	// Every is set for the duration form.
	Every time.Duration
	// The cron fields, each holding the set of values that match. Empty means
	// "any", which is what * parses to.
	minutes, hours, days, months, weekdays map[int]bool
	cron                                   bool
	text                                   string
}

func (s Spec) String() string { return s.text }

// Parse reads a spec, or says what is wrong with it in a sentence an operator
// can act on.
func Parse(text string) (Spec, error) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return Spec{}, errors.New("a schedule needs a spec: either `every 15m` or a five-field cron like `0 7 * * 1-5`")
	}
	if rest, ok := strings.CutPrefix(text, "every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return Spec{}, fmt.Errorf("%q is not a duration: write it like `every 15m`, `every 2h`, `every 24h`", rest)
		}
		if d < MinEvery {
			return Spec{}, fmt.Errorf("`every %s` is faster than this install will schedule (%s is the floor): "+
				"a run holds its workspace while it works, so anything quicker only builds a backlog", rest, MinEvery)
		}
		return Spec{Every: d, text: text}, nil
	}
	return parseCron(text)
}

// The cron ranges, in the order the fields appear.
var cronRange = [5]struct {
	name     string
	min, max int
}{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day of month", 1, 31},
	{"month", 1, 12},
	{"day of week", 0, 6},
}

func parseCron(text string) (Spec, error) {
	fields := strings.Fields(text)
	if len(fields) != 5 {
		return Spec{}, fmt.Errorf("a cron spec has five fields — minute hour day-of-month month day-of-week — and this has %d. "+
			"Write `0 7 * * 1-5` for 07:00 on weekdays, or use `every 15m`", len(fields))
	}
	s := Spec{cron: true, text: strings.Join(fields, " ")}
	sets := [5]*map[int]bool{&s.minutes, &s.hours, &s.days, &s.months, &s.weekdays}
	for i, f := range fields {
		set, err := parseField(f, cronRange[i].min, cronRange[i].max)
		if err != nil {
			return Spec{}, fmt.Errorf("the %s field (%q): %w", cronRange[i].name, f, err)
		}
		*sets[i] = set
	}
	return s, nil
}

// parseField reads one cron field: *, a number, a-b, */n, a-b/n, or a comma
// list of those. Sunday is 0 and only 0 — a spec where 7 also meant Sunday is a
// spec where a typo is silently a valid schedule.
//
// A bare * returns NIL, meaning "any", and that is load-bearing rather than an
// optimisation. Expanding it into the full set makes it indistinguishable from
// an explicit list, and day-of-month and day-of-week are OR'd when both are
// restricted — so `0 7 * * 1-5` became "every weekday OR every day", which is
// every day. It fired on a Saturday, and 30 February resolved to a real date
// for the same reason.
func parseField(f string, min, max int) (map[int]bool, error) {
	if strings.TrimSpace(f) == "*" {
		return nil, nil
	}
	out := map[int]bool{}
	for _, part := range strings.Split(f, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("empty entry in a comma list")
		}
		step := 1
		if base, stepText, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%q is not a step; write `*/5` or `0-30/10`", stepText)
			}
			step, part = n, base
		}
		lo, hi := min, max
		switch {
		case part == "*":
		default:
			loText, hiText, isRange := strings.Cut(part, "-")
			n, err := strconv.Atoi(loText)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number, a range, or *", part)
			}
			lo, hi = n, n
			if isRange {
				m, err := strconv.Atoi(hiText)
				if err != nil {
					return nil, fmt.Errorf("%q is not the end of a range", hiText)
				}
				hi = m
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("values must be between %d and %d, and a range must not run backwards", min, max)
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	return out, nil
}

// Location resolves a timezone name, or says so. Empty means UTC.
func Location(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%q is not a timezone this install knows; use an IANA name like `Europe/Berlin`", name)
	}
	return loc, nil
}

// Next is the first firing strictly after `after`, in the given zone.
//
// The duration form counts from the moment given rather than from a wall-clock
// grid, because "every 15 minutes" means what it says and a grid would make the
// first interval after a restart shorter than the rest.
//
// The cron form searches minute by minute for at most 366 days. That bound is
// what turns an impossible spec — 30 February — into an answer instead of a
// goroutine that never returns.
func (s Spec) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	if !s.cron {
		if s.Every <= 0 {
			return time.Time{}, false
		}
		return after.Add(s.Every), true
	}

	// Start at the next whole minute: a cron spec names minutes, and firing
	// twice within one is not a thing it can mean.
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(366 * 24 * time.Hour)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if s.matches(t) {
			return t, true
		}
	}
	return time.Time{}, false
}

func (s Spec) matches(t time.Time) bool {
	in := func(set map[int]bool, v int) bool { return len(set) == 0 || set[v] }
	if !in(s.minutes, t.Minute()) || !in(s.hours, t.Hour()) || !in(s.months, int(t.Month())) {
		return false
	}
	// Day-of-month and day-of-week are OR'd when both are restricted, which is
	// how every cron has behaved since the original: `0 0 1 * 1` means the
	// first of the month AND every Monday, not their intersection. It is
	// surprising, it is what people's existing specs assume, and quietly
	// differing here would be worse than the surprise.
	dom, dow := len(s.days) > 0, len(s.weekdays) > 0
	switch {
	case dom && dow:
		return s.days[t.Day()] || s.weekdays[int(t.Weekday())]
	case dom:
		return s.days[t.Day()]
	case dow:
		return s.weekdays[int(t.Weekday())]
	}
	return true
}
