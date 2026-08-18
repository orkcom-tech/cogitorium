package metrics

import (
	"strconv"
	"strings"
	"testing"
)

func render(r *Registry) string {
	var b strings.Builder
	r.Write(&b)
	return b.String()
}

func TestACounterCountsAndIsLabelled(t *testing.T) {
	r := New()
	h := r.Register("cog_things_total", "things", Counter)
	h.Inc(map[string]string{"outcome": "ok"})
	h.Inc(map[string]string{"outcome": "ok"})
	h.Inc(map[string]string{"outcome": "failed"})

	out := render(r)
	if !strings.Contains(out, `cog_things_total{outcome="ok"} 2`) {
		t.Fatalf("counter did not count:\n%s", out)
	}
	if !strings.Contains(out, `cog_things_total{outcome="failed"} 1`) {
		t.Fatalf("the second label set is missing:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE cog_things_total counter") {
		t.Fatalf("no TYPE line, so Prometheus reads it as untyped:\n%s", out)
	}
}

// THE ONE THAT CAUGHT A REAL BUG. Prometheus buckets are cumulative — each
// counts everything at or below its bound — and an implementation that both
// tallies into every matching bucket AND cumulates when writing produces a
// histogram where every quantile is the maximum.
func TestHistogramBucketsAreCumulativeExactlyOnce(t *testing.T) {
	r := New()
	h := r.Register("cog_seconds", "how long", Histogram, 1, 5, 10)
	for _, v := range []float64{0.5, 2, 2, 7, 100} {
		h.Observe(nil, v)
	}
	out := render(r)

	// 0.5 <= 1                     -> le=1   is 1
	// plus 2, 2                    -> le=5   is 3
	// plus 7                       -> le=10  is 4
	// plus 100, past every bound   -> +Inf   is 5
	for _, want := range []string{
		`cog_seconds_bucket{le="1"} 1`,
		`cog_seconds_bucket{le="5"} 3`,
		`cog_seconds_bucket{le="10"} 4`,
		`cog_seconds_bucket{le="+Inf"} 5`,
		`cog_seconds_count 5`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// 0.5+2+2+7+100
	if !strings.Contains(out, "cog_seconds_sum 111.5") {
		t.Errorf("sum is wrong in:\n%s", out)
	}
}

// A bucket count must never fall as the bound rises, which is the property a
// double-cumulation breaks silently.
func TestBucketsNeverDecrease(t *testing.T) {
	r := New()
	h := r.Register("cog_x", "x", Histogram, 1, 2, 3, 4)
	for i := range 40 {
		h.Observe(nil, float64(i%5))
	}
	var last int
	for _, line := range strings.Split(render(r), "\n") {
		if !strings.Contains(line, "_bucket{") {
			continue
		}
		n, err := strconv.Atoi(strings.Fields(line)[1])
		if err != nil {
			t.Fatalf("unreadable bucket line %q", line)
		}
		if n < last {
			t.Fatalf("bucket counts fall: %q after %d", line, last)
		}
		last = n
	}
}

func TestAGaugeIsSetRatherThanAdded(t *testing.T) {
	r := New()
	h := r.Register("cog_queued", "waiting", Gauge)
	h.Set(nil, 5)
	h.Set(nil, 2)
	if out := render(r); !strings.Contains(out, "cog_queued 2") {
		t.Fatalf("a gauge accumulated instead of being set:\n%s", out)
	}
}

// A metric nobody touched is not written: an operator reading a zero cannot
// tell "nothing happened" from "this is not wired up".
func TestAnUntouchedMetricIsNotPublished(t *testing.T) {
	r := New()
	r.Register("cog_never", "never used", Counter)
	if out := render(r); strings.Contains(out, "cog_never") {
		t.Fatalf("an untouched metric was published:\n%s", out)
	}
}

// A label value can come from a build stamp or an error class. A raw quote or
// newline in one makes the whole scrape unparseable, not just that line.
func TestLabelValuesAreEscaped(t *testing.T) {
	r := New()
	h := r.Register("cog_build_info", "build", Gauge)
	h.Set(map[string]string{"version": "v1\"2\nbroken\\"}, 1)
	out := render(r)
	if strings.Contains(out, "v1\"2") || strings.Contains(out, "broken\\\n") {
		t.Fatalf("a label value was not escaped:\n%s", out)
	}
	if !strings.Contains(out, `version="v1\"2\nbroken\\"`) {
		t.Fatalf("escaping is wrong:\n%s", out)
	}
}

// Registering twice returns the same metric rather than two that disagree.
func TestRegisteringTwiceIsTheSameMetric(t *testing.T) {
	r := New()
	a := r.Register("cog_dup_total", "one", Counter)
	b := r.Register("cog_dup_total", "two", Counter)
	a.Inc(nil)
	b.Inc(nil)
	if out := render(r); !strings.Contains(out, "cog_dup_total 2") {
		t.Fatalf("two registrations produced two metrics:\n%s", out)
	}
}

// The order of a label map must not change the series a caller lands on.
func TestLabelOrderDoesNotSplitASeries(t *testing.T) {
	r := New()
	h := r.Register("cog_two_total", "two labels", Counter)
	h.Inc(map[string]string{"a": "1", "b": "2"})
	h.Inc(map[string]string{"b": "2", "a": "1"})
	if out := render(r); !strings.Contains(out, `cog_two_total{a="1",b="2"} 2`) {
		t.Fatalf("the same labels produced two series:\n%s", out)
	}
}

// Concurrent counting must not lose an increment; this is the whole reason the
// value is an atomic rather than a plain float.
func TestConcurrentIncrementsAreNotLost(t *testing.T) {
	r := New()
	h := r.Register("cog_race_total", "race", Counter)
	done := make(chan struct{})
	for range 8 {
		go func() {
			for range 500 {
				h.Inc(map[string]string{"k": "v"})
			}
			done <- struct{}{}
		}()
	}
	for range 8 {
		<-done
	}
	if out := render(r); !strings.Contains(out, `cog_race_total{k="v"} 4000`) {
		t.Fatalf("increments were lost:\n%s", out)
	}
}
