package promtext

import (
	"math"
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, path string) Families {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fams, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return fams
}

func loadJSON(t *testing.T, path string) Families {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fams, err := FromQueryJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	return fams
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9*math.Max(1, math.Abs(b)) }

func TestSumUnlabelled(t *testing.T) {
	fams := load(t, "../../testdata/llamacpp/b10809-5266f24da.metrics.txt")
	got, ok := fams.Sum("llamacpp:prompt_tokens_total", nil)
	if !ok || got != 278 {
		t.Fatalf("got %v, %v; want 278", got, ok)
	}
	if !fams.Has("llamacpp:prompt_tokens_cached_total") {
		t.Fatal("cached counter not found")
	}
}

func TestSumAbsentIsNotZero(t *testing.T) {
	fams := load(t, "../../testdata/llamacpp/b10809-5266f24da.metrics.txt")
	if _, ok := fams.Sum("llamacpp:no_such_metric", nil); ok {
		t.Fatal("an absent metric reported found")
	}
	// A filter that matches nothing is also "not found", not zero.
	if _, ok := fams.Sum("llamacpp:prompt_tokens_total", LabelIs("model", "x")); ok {
		t.Fatal("a filter matching nothing reported found")
	}
}

func TestSumWithFilter(t *testing.T) {
	fams := load(t, "../../testdata/sglang/nightly-dev-cu13-20260921-0f6761b5.metrics.txt")
	got, ok := fams.Sum("sglang:realtime_tokens_total", LabelIs("mode", "decode"))
	if !ok || got != 156755 {
		t.Fatalf("got %v, %v; want 156755", got, ok)
	}
	// Two label sets (is_streaming true and false) are summed.
	got, ok = fams.Sum("sglang:prompt_tokens_total", nil)
	if !ok || got != 112551+4555486 {
		t.Fatalf("got %v, %v; want %v", got, ok, 112551+4555486)
	}
}

func TestAllOf(t *testing.T) {
	m := AllOf(LabelIs("a", "1"), LabelIn("b", "", "2"))
	if !m(map[string]string{"a": "1"}) {
		t.Error("missing b should match the empty value")
	}
	if !m(map[string]string{"a": "1", "b": "2"}) {
		t.Error("b=2 should match")
	}
	if m(map[string]string{"a": "1", "b": "3"}) {
		t.Error("b=3 should not match")
	}
	if m(map[string]string{"a": "2"}) {
		t.Error("a=2 should not match")
	}
}

func TestHistogramSumsLabelSets(t *testing.T) {
	fams := load(t, "../../testdata/sglang/nightly-dev-cu13-20260921-0f6761b5.metrics.txt")
	h, ok, err := fams.Histogram("sglang:time_to_first_token_seconds", nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if h.Count != 9+174 {
		t.Errorf("count %d, want 183", h.Count)
	}
	if !approx(h.Sum, 109.42931875799695+744.9516995760059) {
		t.Errorf("sum %v", h.Sum)
	}
	if len(h.Buckets) == 0 {
		t.Error("no buckets")
	}
}

func TestLabelValues(t *testing.T) {
	fams := load(t, "../../testdata/sglang/nightly-dev-cu13-20260921-0f6761b5.metrics.txt")
	got := fams.LabelValues("sglang:generation_tokens_total", "model_name")
	if len(got) != 1 || got[0] != "MiMo-v2.6-Flash" {
		t.Fatalf("got %v", got)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse(strings.NewReader("this is { not metrics\n")); err == nil {
		t.Fatal("parsed garbage")
	}
}

func TestParseEmptyIsNoFamilies(t *testing.T) {
	fams, err := Parse(strings.NewReader(""))
	if err != nil || len(fams) != 0 {
		t.Fatalf("fams=%v err=%v", fams, err)
	}
}

func TestFromQueryJSONInfersTypes(t *testing.T) {
	fams := loadJSON(t, "../../testdata/vllm/stored-gpt-oss-20b.promql.json")
	got, ok := fams.Sum("vllm:prompt_tokens_by_source_total", LabelIs("source", "local_compute"))
	if !ok || got != 363231 {
		t.Fatalf("local_compute %v, %v; want 363231", got, ok)
	}
	h, ok, err := fams.Histogram("vllm:request_prefill_time_seconds", nil)
	if err != nil || !ok {
		t.Fatalf("histogram ok=%v err=%v", ok, err)
	}
	if !approx(h.Sum, 70.97365798191458) {
		t.Errorf("prefill seconds sum %v", h.Sum)
	}
	// iteration_tokens_total is a histogram whose base name ends in _total.
	if _, ok, err := fams.Histogram("vllm:iteration_tokens_total", nil); !ok || err != nil {
		t.Errorf("iteration_tokens_total ok=%v err=%v", ok, err)
	}
}

func TestFromQueryJSONRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "nope",
		"error status":  `{"status":"error","data":{"resultType":"vector","result":[]}}`,
		"matrix result": `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		"no name":       `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"2"]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FromQueryJSON([]byte(body)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestEscapeRoundTrips(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"x_total","l":"a\"b\\c\nd"},"value":[1,"2"]}]}}`
	fams, err := FromQueryJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := fams.LabelValues("x_total", "l"); len(got) != 1 || got[0] != "a\"b\\c\nd" {
		t.Fatalf("got %q", got)
	}
}
