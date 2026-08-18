// Package metrics is what an operator can alert on.
//
// # Why this is written out rather than imported
//
// This binary has six direct dependencies and says so as a feature.
// `client_golang` pulls a tree for what is, in the end, counters in a map and a
// few hundred bytes of text on an HTTP endpoint. The precedent is already here:
// openapi.go renders OpenAPI by hand for the same reason and says so.
//
// There is a second reason and it is the better one. The usual library carries
// a global default registry that collects the Go runtime and the process, and
// everything that imports anything ends up registering into it. Here, what is
// exposed is a list in one file that somebody chose.
//
// # The rule about labels, which is the only dangerous part
//
// A LABEL VALUE IS PUBLISHED TO WHOEVER CAN SCRAPE. `runs_total{workspace=
// "acme-payroll"}` hands the names of every workspace to a monitoring system
// that is usually less guarded than the product, and to everyone who can read a
// dashboard. So nothing here is labelled by workspace, agent, model, tool,
// user, or any other name a person chose.
//
// The second reason is cardinality: a label whose values are unbounded turns a
// time series database into a memory leak, and "one series per workspace per
// agent" is unbounded by construction. Per-workspace spend is already in the
// database and on the People map, which are authenticated surfaces with an
// audience.
package metrics

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Kind is what a metric IS, which decides how Prometheus reads it.
type Kind string

const (
	Counter   Kind = "counter"
	Gauge     Kind = "gauge"
	Histogram Kind = "histogram"
)

// Registry holds every metric this process publishes.
type Registry struct {
	mu sync.RWMutex
	// ordered so the exposition is stable: a diff between two scrapes should be
	// about the numbers.
	names   []string
	metrics map[string]*metric
}

func New() *Registry {
	return &Registry{metrics: map[string]*metric{}}
}

type metric struct {
	name string
	help string
	kind Kind
	// buckets are the upper bounds of a histogram, ascending. Nil otherwise.
	buckets []float64

	mu     sync.RWMutex
	series map[string]*series
}

// series is one label combination.
type series struct {
	labels map[string]string
	// value is the counter's count or the gauge's level, as bits of a float so
	// it can be added to without a lock on the hot path.
	value atomic.Uint64
	// counts and sum belong to a histogram and are guarded by the metric's own
	// lock, because they have to move together.
	counts []uint64
	sum    float64
}

func (s *series) add(v float64) {
	for {
		old := s.value.Load()
		next := floatBits(bitsFloat(old) + v)
		if s.value.CompareAndSwap(old, next) {
			return
		}
	}
}

func (s *series) set(v float64) { s.value.Store(floatBits(v)) }
func (s *series) get() float64  { return bitsFloat(s.value.Load()) }

// A float in an atomic word, so a counter can be added to without taking a
// lock on the hot path.
func floatBits(f float64) uint64 { return math.Float64bits(f) }
func bitsFloat(u uint64) float64 { return math.Float64frombits(u) }

// Register declares a metric. Declaring the same name twice returns the first,
// so a package that registers in an init and a caller that registers on
// construction cannot fight.
func (r *Registry) Register(name, help string, kind Kind, buckets ...float64) *Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.metrics[name]; ok {
		return &Handle{m: m}
	}
	m := &metric{name: name, help: help, kind: kind, series: map[string]*series{}}
	if kind == Histogram {
		if len(buckets) == 0 {
			buckets = DefaultBuckets
		}
		m.buckets = slices.Clone(buckets)
		sort.Float64s(m.buckets)
	}
	r.metrics[name] = m
	r.names = append(r.names, name)
	sort.Strings(r.names)
	return &Handle{m: m}
}

// DefaultBuckets spans a model call and a gear run rather than a web request:
// this product's interesting latencies are seconds to minutes, and the usual
// millisecond-scale default would put everything in +Inf.
var DefaultBuckets = []float64{0.05, 0.25, 1, 2.5, 5, 10, 30, 60, 300}

// Handle is what a caller keeps. Registering once and holding the handle is the
// whole of the fast path.
type Handle struct{ m *metric }

// With returns the series for one label combination, creating it once.
func (h *Handle) With(labels map[string]string) *series {
	key := labelKey(labels)
	h.m.mu.RLock()
	s, ok := h.m.series[key]
	h.m.mu.RUnlock()
	if ok {
		return s
	}
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if s, ok := h.m.series[key]; ok {
		return s
	}
	s = &series{labels: maps.Clone(labels)}
	if h.m.kind == Histogram {
		s.counts = make([]uint64, len(h.m.buckets))
	}
	h.m.series[key] = s
	return s
}

// Inc adds one to a counter.
func (h *Handle) Inc(labels map[string]string) { h.With(labels).add(1) }

// Add adds to a counter or a gauge.
func (h *Handle) Add(labels map[string]string, v float64) { h.With(labels).add(v) }

// Set writes a gauge.
func (h *Handle) Set(labels map[string]string, v float64) { h.With(labels).set(v) }

// Observe records one measurement into a histogram.
func (h *Handle) Observe(labels map[string]string, v float64) {
	s := h.With(labels)
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s.sum += v
	// The FIRST bucket this falls in, and only that one. counts is a per-bucket
	// tally here and is made cumulative when it is written — incrementing every
	// bucket at or above the value would make it cumulative twice, which reads
	// as a histogram where every quantile is the maximum.
	//
	// A value past the last bound lands in no bucket at all, which is correct:
	// it is counted by +Inf, and +Inf is the total.
	for i, b := range h.m.buckets {
		if v <= b {
			s.counts[i]++
			break
		}
	}
	// The count lives in the same word every other kind uses, so the exposition
	// does not need a special case for "how many".
	s.add(1)
}

// labelKey is a stable identity for a label set. Sorted, because a map is not
// ordered and two calls with the same labels must find the same series.
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(labels))
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\x00')
		b.WriteString(labels[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// Write renders the whole registry in the Prometheus text exposition format.
func (r *Registry) Write(w *strings.Builder) {
	r.mu.RLock()
	names := slices.Clone(r.names)
	byName := maps.Clone(r.metrics)
	r.mu.RUnlock()

	for _, name := range names {
		m := byName[name]
		m.mu.RLock()
		keys := slices.Sorted(maps.Keys(m.series))
		m.mu.RUnlock()
		if len(keys) == 0 {
			// A metric nobody has touched is not written. An operator reading a
			// zero cannot tell "nothing happened" from "this is not wired up",
			// and the second is the one worth noticing.
			continue
		}

		fmt.Fprintf(w, "# HELP %s %s\n", name, m.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", name, m.kind)
		for _, key := range keys {
			m.mu.RLock()
			s := m.series[key]
			m.mu.RUnlock()
			if m.kind != Histogram {
				fmt.Fprintf(w, "%s%s %s\n", name, renderLabels(s.labels, nil), num(s.get()))
				continue
			}
			m.mu.RLock()
			counts := slices.Clone(s.counts)
			sum := s.sum
			m.mu.RUnlock()
			// Prometheus histograms are CUMULATIVE: each bucket counts
			// everything at or below its bound, so the running total is what is
			// written rather than the per-bucket tally.
			var running uint64
			for i, b := range m.buckets {
				running += counts[i]
				fmt.Fprintf(w, "%s_bucket%s %d\n", name,
					renderLabels(s.labels, map[string]string{"le": num(b)}), running)
			}
			total := uint64(s.get())
			fmt.Fprintf(w, "%s_bucket%s %d\n", name,
				renderLabels(s.labels, map[string]string{"le": "+Inf"}), total)
			fmt.Fprintf(w, "%s_sum%s %s\n", name, renderLabels(s.labels, nil), num(sum))
			fmt.Fprintf(w, "%s_count%s %d\n", name, renderLabels(s.labels, nil), total)
		}
	}
}

func renderLabels(labels map[string]string, extra map[string]string) string {
	all := make(map[string]string, len(labels)+len(extra))
	maps.Copy(all, labels)
	maps.Copy(all, extra)
	if len(all) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(all))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+`="`+escape(all[k])+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// escape is the exposition format's own rule. A label value that carried a raw
// newline or quote would make the whole scrape unparseable, and a value can
// come from a build stamp or an error class rather than from this file.
func escape(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// num renders a float the way Prometheus reads it: no exponent for ordinary
// magnitudes, and no trailing zeros.
func num(f float64) string {
	if f == float64(int64(f)) && f < 1e15 && f > -1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
