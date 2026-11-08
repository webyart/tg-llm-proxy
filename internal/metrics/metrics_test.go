package metrics

import (
	"bytes"
	"math"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func render(t *testing.T, reg *Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := reg.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.String()
}

var sampleLine = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^}]*\})? (NaN|[+-]?Inf|[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)$`)

// assertValidExposition checks the shape of every line: HELP/TYPE lines only before the samples of a
// family, and every sample is name{labels} value.
func assertValidExposition(t *testing.T, text string) {
	t.Helper()
	var seen = map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			var fields = strings.SplitN(line, " ", 4)
			if len(fields) < 4 {
				t.Fatalf("malformed HELP line %q", line)
			}
			seen[fields[2]+":help"] = true
		case strings.HasPrefix(line, "# TYPE "):
			var fields = strings.SplitN(line, " ", 4)
			if len(fields) != 4 {
				t.Fatalf("malformed TYPE line %q", line)
			}
			switch fields[3] {
			case "counter", "gauge", "histogram":
			default:
				t.Fatalf("unknown type in %q", line)
			}
			seen[fields[2]+":type"] = true
		default:
			if !sampleLine.MatchString(line) {
				t.Fatalf("not a sample line: %q", line)
			}
			var name = line[:strings.IndexAny(line, "{ ")]
			if !seen[name+":type"] && !strings.HasSuffix(name, "_bucket") && !strings.HasSuffix(name, "_sum") && !strings.HasSuffix(name, "_count") {
				t.Fatalf("sample %q has no TYPE line", line)
			}
		}
	}
}

func TestTextFormatIsValid(t *testing.T) {
	var reg = NewRegistry()
	reg.Counter("requests_total", "Requests.").Add(map[string]string{"result": "ok"}, 2)
	reg.Gauge("queue_depth", "Queued updates.").Set(map[string]string{"chat_id": "42"}, 3)
	reg.Histogram("latency_seconds", "Latency.", []float64{1, 0.1}).Observe(nil, 0.5)

	var text = render(t, reg)
	assertValidExposition(t, text)

	var want = "" +
		"# HELP latency_seconds Latency.\n" +
		"# TYPE latency_seconds histogram\n" +
		`latency_seconds_bucket{le="0.1"} 0` + "\n" +
		`latency_seconds_bucket{le="1"} 1` + "\n" +
		`latency_seconds_bucket{le="+Inf"} 1` + "\n" +
		"latency_seconds_sum 0.5\n" +
		"latency_seconds_count 1\n" +
		"# HELP queue_depth Queued updates.\n" +
		"# TYPE queue_depth gauge\n" +
		`queue_depth{chat_id="42"} 3` + "\n" +
		"# HELP requests_total Requests.\n" +
		"# TYPE requests_total counter\n" +
		`requests_total{result="ok"} 2` + "\n"
	if text != want {
		t.Fatalf("exposition:\n%s\nwant:\n%s", text, want)
	}
}

func TestLabelAndHelpEscaping(t *testing.T) {
	var reg = NewRegistry()
	reg.Counter("escaped_total", "Help with \\ and \n newline.").Add(map[string]string{"label": "a\"b\\c\nd"}, 1)

	var text = render(t, reg)
	assertValidExposition(t, text)
	if !strings.Contains(text, `# HELP escaped_total Help with \\ and \n newline.`) {
		t.Errorf("help is not escaped: %q", text)
	}
	if !strings.Contains(text, `escaped_total{label="a\"b\\c\nd"} 1`) {
		t.Errorf("label value is not escaped: %q", text)
	}
}

func TestHistogramBucketsCountAndSum(t *testing.T) {
	var reg = NewRegistry()
	var h = reg.Histogram("t_seconds", "Time.", []float64{1, 0.1})
	h.Observe(nil, 0.0625)
	h.Observe(nil, 0.5)
	h.Observe(nil, 2)
	var text = render(t, reg)
	assertValidExposition(t, text)

	for _, want := range []string{
		`t_seconds_bucket{le="0.1"} 1`,
		`t_seconds_bucket{le="1"} 2`,
		`t_seconds_bucket{le="+Inf"} 3`,
		"t_seconds_count 3",
		"t_seconds_sum 2.5625",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	var build = func(reverse bool) string {
		var reg = NewRegistry()
		if reverse {
			reg.Gauge("z_depth", "Depth.").Set(map[string]string{"chat_id": "2"}, 2)
			reg.Counter("a_total", "Total.").Add(map[string]string{"result": "ok"}, 1)
		} else {
			reg.Counter("a_total", "Total.").Add(map[string]string{"result": "ok"}, 1)
			reg.Gauge("z_depth", "Depth.").Set(map[string]string{"chat_id": "2"}, 2)
		}
		reg.Counter("a_total", "Total.").Add(map[string]string{"result": "error"}, 3)
		return render(t, reg)
	}
	var first = build(false)
	var second = build(true)
	if first != second {
		t.Fatalf("the exposition depends on registration order:\n%s\nvs\n%s", first, second)
	}
	var again = build(false)
	if again != first {
		t.Fatalf("two renders of the same state differ:\n%s\nvs\n%s", first, again)
	}
}

func TestLabelSetsAreSorted(t *testing.T) {
	var reg = NewRegistry()
	var counter = reg.Counter("c_total", "C.")
	counter.Add(map[string]string{"chat_id": "2"}, 1)
	counter.Add(map[string]string{"chat_id": "10"}, 1)
	var text = render(t, reg)
	if strings.Index(text, `chat_id="10"`) > strings.Index(text, `chat_id="2"`) {
		t.Fatalf("label sets are not sorted by value:\n%s", text)
	}
}

func TestConcurrentWriters(t *testing.T) {
	var reg = NewRegistry()
	var counter = reg.Counter("c_total", "C.")
	var gauge = reg.Gauge("g", "G.")
	var hist = reg.Histogram("h_seconds", "H.", []float64{1})
	reg.GaugeFunc("live", "Live.", func() float64 { return 1 })

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				counter.Add(nil, 1)
				gauge.Add(nil, 1)
				hist.Observe(nil, 0.5)
				_, _ = reg.WriteTo(&bytes.Buffer{})
			}
		}(worker)
	}
	wg.Wait()

	var text = render(t, reg)
	assertValidExposition(t, text)
	for _, want := range []string{"c_total 800", "g 800", "h_seconds_count 800", "live 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestGaugeFuncIsEvaluatedAtScrape(t *testing.T) {
	var reg = NewRegistry()
	var value atomic.Int64
	value.Store(7)
	reg.GaugeFunc("live_value", "Live.", func() float64 { return float64(value.Load()) })

	if text := render(t, reg); !strings.Contains(text, "live_value 7") {
		t.Fatalf("first render: %s", text)
	}
	value.Store(11)
	if text := render(t, reg); !strings.Contains(text, "live_value 11") {
		t.Fatalf("the gauge function was not called at scrape time: %s", text)
	}
}

func TestHandlerServesTextFormat(t *testing.T) {
	var reg = NewRegistry()
	reg.Counter("hits_total", "Hits.").Add(nil, 4)

	var recorder = httptest.NewRecorder()
	Handler(reg).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("Content-Type = %q", got)
	}
	if recorder.Body.String() != "# HELP hits_total Hits.\n# TYPE hits_total counter\nhits_total 4\n" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestEmptyRegistryRendersNothing(t *testing.T) {
	if text := render(t, NewRegistry()); text != "" {
		t.Fatalf("empty registry rendered %q", text)
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var reg *Registry
	reg.Counter("x_total", "X.").Add(nil, 1)
	reg.Gauge("x", "X.").Set(nil, 1)
	reg.Histogram("x_seconds", "X.", nil).Observe(nil, 1)
	var buf bytes.Buffer
	if n, err := reg.WriteTo(&buf); n != 0 || err != nil {
		t.Fatalf("WriteTo on nil = %d, %v", n, err)
	}
}

func TestFloatFormatting(t *testing.T) {
	var cases = map[float64]string{
		1:            "1",
		0.5:          "0.5",
		2.55:         "2.55",
		1e6:          "1e+06",
		math.Inf(1):  "+Inf",
		math.Inf(-1): "-Inf",
	}
	for value, want := range cases {
		if got := formatFloat(value); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", value, got, want)
		}
	}
}
