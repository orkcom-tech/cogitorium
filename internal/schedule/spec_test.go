package schedule

import (
	"strings"
	"testing"
	"time"
)

// What a spec means, checked against the clock rather than against itself.
//
// Every case here is a sentence an operator could type, and the assertion is
// the instant it next fires. A parser that accepts a spec and then means
// something else is worse than one that refuses it: the schedule looks right in
// the interface and runs at the wrong time, once, at night.
func TestWhenASpecNextFires(t *testing.T) {
	t.Parallel()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("no zoneinfo, so this install would resolve every schedule to UTC: %v", err)
	}

	for _, c := range []struct {
		name, spec string
		loc        *time.Location
		after      time.Time
		want       time.Time
	}{
		{
			name: "every fifteen minutes counts from now, not from a grid",
			spec: "every 15m", loc: time.UTC,
			after: time.Date(2026, 3, 4, 10, 7, 30, 0, time.UTC),
			want:  time.Date(2026, 3, 4, 10, 22, 30, 0, time.UTC),
		},
		{
			name: "seven in the morning on weekdays, from a Friday afternoon, is Monday",
			spec: "0 7 * * 1-5", loc: time.UTC,
			after: time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC), // a Friday
			want:  time.Date(2026, 3, 9, 7, 0, 0, 0, time.UTC),  // the Monday
		},
		{
			name: "a step fires on the step, not on every minute",
			spec: "*/20 * * * *", loc: time.UTC,
			after: time.Date(2026, 3, 4, 10, 5, 0, 0, time.UTC),
			want:  time.Date(2026, 3, 4, 10, 20, 0, 0, time.UTC),
		},
		{
			name: "a zone is the operator's zone, not the server's",
			spec: "30 6 * * *", loc: berlin,
			after: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			// 06:30 in Berlin in January is 05:30 UTC.
			want: time.Date(2026, 1, 16, 5, 30, 0, 0, time.UTC),
		},
		{
			name: "a time that does not exist on the day the clocks go forward is skipped, once",
			spec: "30 2 * * *", loc: berlin,
			// 2026-03-29 is the spring change in Berlin: 02:00 becomes 03:00,
			// so 02:30 does not happen at all that day. The honest behaviour is
			// to skip that day and fire the next one — NOT to invent a time, and
			// not to stop scheduling. This expectation was written the other way
			// round first, and the code was right.
			after: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 3, 30, 0, 30, 0, 0, time.UTC),
		},
		{
			name: "and the day the clocks go back fires once, not twice",
			spec: "30 2 * * *", loc: berlin,
			// 2026-10-25: 03:00 becomes 02:00, so 02:30 happens twice. The
			// search returns the first, and having returned it the schedule's
			// next_at moves past both.
			after: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, err := Parse(c.spec)
			if err != nil {
				t.Fatalf("parse %q: %v", c.spec, err)
			}
			got, ok := s.Next(c.after, c.loc)
			if !ok {
				t.Fatalf("%q never fires after %s", c.spec, c.after)
			}
			if !got.Equal(c.want) {
				t.Fatalf("%q after %s fires at %s, want %s",
					c.spec, c.after.Format(time.RFC3339), got.UTC().Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

// What is refused, and whether the refusal tells the operator what to write.
//
// This is the moment they are still looking at what they typed, so a message
// that only says "invalid" sends them to read source they do not have.
func TestASpecThatCannotWorkIsRefusedWithASentence(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ spec, says string }{
		{"", "either `every 15m`"},
		{"every 30s", "is the floor"},
		{"every banana", "is not a duration"},
		{"0 7 * *", "five fields"},
		{"0 7 * * 1-5 *", "five fields"},
		{"60 * * * *", "between 0 and 59"},
		{"* 24 * * *", "between 0 and 23"},
		{"* * 0 * *", "between 1 and 31"},
		{"* * * 13 *", "between 1 and 12"},
		{"* * * * 7", "between 0 and 6"},
		{"5-1 * * * *", "must not run backwards"},
		{"*/0 * * * *", "is not a step"},
		{"a * * * *", "is not a number"},
		{"1,,2 * * * *", "empty entry"},
	} {
		err := func() error { _, err := Parse(c.spec); return err }()
		if err == nil {
			t.Fatalf("%q was accepted", c.spec)
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Fatalf("refusing %q does not say what to write instead: %v", c.spec, err)
		}
	}
}

// An impossible date answers rather than searching forever.
func TestASpecThatCanNeverFireSaysSo(t *testing.T) {
	t.Parallel()
	s, err := Parse("0 0 30 2 *") // 30 February
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := s.Next(time.Now().UTC(), time.UTC); ok {
		t.Fatal("30 February was reported as a date")
	}
}

// Day-of-month and day-of-week are OR'd when both are given. Surprising, and
// what every existing cron spec assumes — quietly differing would be worse.
func TestDayOfMonthAndDayOfWeekAreOred(t *testing.T) {
	t.Parallel()
	s, err := Parse("0 0 1 * 1") // the 1st, and every Monday
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 2026-03-02 is a Monday and not the 1st.
	got, ok := s.Next(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), time.UTC)
	if !ok {
		t.Fatal("never fires")
	}
	want := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("fires at %s, want %s (a Monday that is not the 1st)", got, want)
	}
}

// A timezone name this install does not know is refused rather than silently
// becoming UTC, which is how a schedule fires an hour off and nobody notices.
func TestAnUnknownTimezoneIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := Location("Mars/Olympus"); err == nil {
		t.Fatal("an unknown zone was accepted")
	}
	if loc, err := Location(""); err != nil || loc != time.UTC {
		t.Fatalf("an empty zone should be UTC: %v %v", loc, err)
	}
	if _, err := Location("Europe/Berlin"); err != nil {
		t.Fatalf("a real zone was refused, so tzdata is not embedded: %v", err)
	}
}
