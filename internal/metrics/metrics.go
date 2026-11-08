// Package metrics is a dependency-free Prometheus text exposition.
//
// The module has exactly two direct dependencies and this package keeps that number: the exposition
// format is a few lines of text, so a full client library is not worth a third one. Output is sorted by
// family and by label set, so two renders of the same state are byte-identical and a test can compare
// them without parsing.
package metrics

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Default is a process-wide registry. Components should prefer an explicitly passed *Registry so that
// tests stay isolated; Default exists for callers that have no owner to pass one from.
var Default = NewRegistry()

// DefaultBuckets are used by Histogram when the caller passes none. They cover a chat request that is
// fast at the low end and a thinking model at the high end.
var DefaultBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300}

// Registry holds counters, gauges and histograms by name and writes them in the text exposition format.
// It is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

type family struct {
	reg     *Registry
	name    string
	help    string
	typ     string
	buckets []float64
	samples map[string]*sample
	// fn turns a plain gauge into a value computed at scrape time, so an atomic counter of a component
	// can be exposed without a polling goroutine.
	fn func() float64
}

type sample struct {
	labels map[string]string
	value  float64
	counts []uint64 // histogram: per bucket, not cumulative
	sum    float64
	count  uint64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{families: map[string]*family{}}
}

// Counter returns a handle to the counter family. Adding to a counter of another type panics: two
// components registering the same name differently is a wiring bug, not a runtime condition.
func (r *Registry) Counter(name, help string) *Counter {
	return &Counter{fam: r.family(name, help, "counter")}
}

// Gauge returns a handle to the gauge family.
func (r *Registry) Gauge(name, help string) *Gauge {
	return &Gauge{fam: r.family(name, help, "gauge")}
}

// GaugeFunc registers a gauge whose value is computed on every scrape. It is meant for atomics a
// component already maintains; it must not call back into any registry.
func (r *Registry) GaugeFunc(name, help string, fn func() float64) {
	if fn == nil {
		return
	}
	f := r.family(name, help, "gauge")
	if f == nil {
		return
	}
	if f.fn != nil || len(f.samples) > 0 {
		panic("metrics: " + name + " is already a gauge with samples or a function")
	}
	f.fn = fn
}

// Histogram returns a handle to the histogram family. A nil or empty buckets slice gets DefaultBuckets.
func (r *Registry) Histogram(name, help string, buckets []float64) *Histogram {
	f := r.family(name, help, "histogram")
	if f == nil {
		return &Histogram{}
	}
	if f.buckets == nil {
		f.buckets = normalizeBuckets(buckets)
	}
	return &Histogram{fam: f}
}

// Handler serves the registry in the Prometheus text format.
func Handler(r *Registry) http.Handler {
	if r == nil {
		r = Default
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = r.WriteTo(w)
	})
}

// Counter is a monotonically increasing sample family.
type Counter struct{ fam *family }

// Add increases every sample selected by labels by v.
func (c *Counter) Add(labels map[string]string, v float64) {
	if c == nil || c.fam == nil {
		return
	}
	c.fam.reg.mu.Lock()
	defer c.fam.reg.mu.Unlock()
	c.fam.writable("counter")
	c.fam.sample(labels).value += v
}

// Gauge is a sample family that can go up and down.
type Gauge struct{ fam *family }

// Set replaces the value of the sample selected by labels.
func (g *Gauge) Set(labels map[string]string, v float64) {
	if g == nil || g.fam == nil {
		return
	}
	g.fam.reg.mu.Lock()
	defer g.fam.reg.mu.Unlock()
	g.fam.writable("gauge")
	g.fam.sample(labels).value = v
}

// Add increases the value of the sample selected by labels by v (v may be negative).
func (g *Gauge) Add(labels map[string]string, v float64) {
	if g == nil || g.fam == nil {
		return
	}
	g.fam.reg.mu.Lock()
	defer g.fam.reg.mu.Unlock()
	g.fam.writable("gauge")
	g.fam.sample(labels).value += v
}

// Histogram is a sample family of cumulative buckets plus a sum and a count.
type Histogram struct{ fam *family }

// Observe records one value.
func (h *Histogram) Observe(labels map[string]string, v float64) {
	if h == nil || h.fam == nil {
		return
	}
	h.fam.reg.mu.Lock()
	defer h.fam.reg.mu.Unlock()
	s := h.fam.sample(labels)
	for i, bound := range h.fam.buckets {
		if v <= bound {
			s.counts[i]++
		}
	}
	s.sum += v
	s.count++
}

// WriteTo renders the registry. The result is deterministic for a given state.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	if r == nil {
		return 0, nil
	}
	r.mu.Lock()
	var buf bytes.Buffer
	r.render(&buf)
	r.mu.Unlock()
	n, err := w.Write(buf.Bytes())
	return int64(n), err
}

// family returns the family for name, creating it when absent.
func (r *Registry) family(name, help, typ string) *family {
	if r == nil {
		return nil
	}
	if !validMetricName(name) {
		panic("metrics: invalid metric name " + strconv.Quote(name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		if f.typ != typ {
			panic("metrics: " + name + " is already registered as a " + f.typ)
		}
		if f.help == "" {
			f.help = help
		}
		return f
	}
	var f = &family{reg: r, name: name, help: help, typ: typ, samples: map[string]*sample{}}
	r.families[name] = f
	return f
}

// writable checks that the family may be written as typ. The registry lock is held by the caller.
func (f *family) writable(typ string) {
	if f.typ != typ {
		panic("metrics: " + f.name + " is a " + f.typ + ", not a " + typ)
	}
	if f.fn != nil {
		panic("metrics: " + f.name + " is a gauge function")
	}
}

// sample returns the sample for labels, creating it when absent. The registry lock is held by the
// caller.
func (f *family) sample(labels map[string]string) *sample {
	key := labelsKey(labels)
	if s, ok := f.samples[key]; ok {
		return s
	}
	for name := range labels {
		if !validLabelName(name) {
			panic("metrics: invalid label name " + strconv.Quote(name) + " on " + f.name)
		}
		if f.typ == "histogram" && name == "le" {
			panic("metrics: " + f.name + " is a histogram, the le label is reserved")
		}
	}
	var copied = make(map[string]string, len(labels))
	for k, v := range labels {
		copied[k] = v
	}
	var s = &sample{labels: copied}
	if f.typ == "histogram" {
		s.counts = make([]uint64, len(f.buckets))
	}
	f.samples[key] = s
	return s
}

func (r *Registry) render(buf *bytes.Buffer) {
	var names = make([]string, 0, len(r.families))
	for name := range r.families {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var f = r.families[name]
		fmt.Fprintf(buf, "# HELP %s %s\n", f.name, escapeHelp(f.help))
		fmt.Fprintf(buf, "# TYPE %s %s\n", f.name, f.typ)
		switch f.typ {
		case "histogram":
			r.renderHistogram(buf, f)
		default:
			if f.fn != nil {
				writeSample(buf, f.name, nil, f.fn())
				continue
			}
			var keys = make([]string, 0, len(f.samples))
			for key := range f.samples {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				return lessLabels(f.samples[keys[i]].labels, f.samples[keys[j]].labels)
			})
			for _, key := range keys {
				var s = f.samples[key]
				writeSample(buf, f.name, s.labels, s.value)
			}
		}
	}
}

func (r *Registry) renderHistogram(buf *bytes.Buffer, f *family) {
	var keys = make([]string, 0, len(f.samples))
	for key := range f.samples {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return lessLabels(f.samples[keys[i]].labels, f.samples[keys[j]].labels)
	})
	for _, key := range keys {
		var s = f.samples[key]
		for i, bound := range f.buckets {
			writeSample(buf, f.name+"_bucket", withLabel(s.labels, "le", formatFloat(bound)), float64(s.counts[i]))
		}
		writeSample(buf, f.name+"_bucket", withLabel(s.labels, "le", "+Inf"), float64(s.count))
		writeSample(buf, f.name+"_sum", s.labels, s.sum)
		writeSample(buf, f.name+"_count", s.labels, float64(s.count))
	}
}

// withLabel returns a copy of labels with one extra entry, so the stored sample is never mutated.
func withLabel(labels map[string]string, name, value string) map[string]string {
	var out = make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out[name] = value
	return out
}

func writeSample(buf *bytes.Buffer, name string, labels map[string]string, value float64) {
	buf.WriteString(name)
	if len(labels) > 0 {
		buf.WriteByte('{')
		var names = make([]string, 0, len(labels))
		for label := range labels {
			names = append(names, label)
		}
		sort.Strings(names)
		for i, label := range names {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(label)
			buf.WriteString(`="`)
			buf.WriteString(escapeLabel(labels[label]))
			buf.WriteByte('"')
		}
		buf.WriteByte('}')
	}
	buf.WriteByte(' ')
	buf.WriteString(formatFloat(value))
	buf.WriteByte('\n')
}

// lessLabels orders samples by label names and values, so output is stable and human-sorted.
func lessLabels(a, b map[string]string) bool {
	var namesA = sortedNames(a)
	var namesB = sortedNames(b)
	for i := 0; i < len(namesA) && i < len(namesB); i++ {
		if namesA[i] != namesB[i] {
			return namesA[i] < namesB[i]
		}
		if a[namesA[i]] != b[namesB[i]] {
			return a[namesA[i]] < b[namesB[i]]
		}
	}
	return len(namesA) < len(namesB)
}

func sortedNames(labels map[string]string) []string {
	var names = make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// labelsKey builds a collision-free key for a label set.
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	var names = make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "%d:%s%d:%s;", len(name), name, len(labels[name]), labels[name])
	}
	return b.String()
}

func normalizeBuckets(buckets []float64) []float64 {
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}
	var out = make([]float64, 0, len(buckets))
	for _, bound := range buckets {
		if math.IsInf(bound, 1) || math.IsNaN(bound) {
			continue
		}
		out = append(out, bound)
	}
	sort.Float64s(out)
	var unique = out[:0]
	for i, bound := range out {
		if i > 0 && bound == out[i-1] {
			continue
		}
		unique = append(unique, bound)
	}
	if len(unique) == 0 {
		return append([]float64(nil), DefaultBuckets...)
	}
	return unique
}

func escapeLabel(value string) string {
	if !strings.ContainsAny(value, "\\\"\n") {
		return value
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}

func escapeHelp(value string) string {
	if !strings.ContainsAny(value, "\\\n") {
		return value
	}
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(value)
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// validMetricName reports whether name matches [a-zA-Z_:][a-zA-Z0-9_:]*.
func validMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// validLabelName reports whether name matches [a-zA-Z_][a-zA-Z0-9_]*.
func validLabelName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
