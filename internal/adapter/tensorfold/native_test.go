package tensorfold

import (
	"strings"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	at "github.com/evanwtf/llm-metrics-exporter/internal/adapter/adaptertest"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
	"github.com/evanwtf/llm-metrics-exporter/internal/promtext"
)

const native = "../../../testdata/tensorfold/v0.6.0-miaai-glm-v1.8.metrics.txt"

// Expected values are read from the fixture with grep, not from MapNative.
func TestNativeLive(t *testing.T) {
	for name, mapFn := range map[string]func(promtext.Families, string) (adapter.Result, error){
		"MapNative": MapNative, "Map": Map,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := mapFn(at.LoadText(t, native), served)
			if err != nil {
				t.Fatal(err)
			}
			s := res.Samples
			at.Valid(t, s)
			at.Want(t, s, &metrics.Tokens, 504646, metrics.Decode)
			at.Want(t, s, &metrics.PromptCachedTokens, 39296704)
			at.Want(t, s, &metrics.RequestPhaseSeconds, 1232.6248, metrics.Prefill)
			at.Want(t, s, &metrics.RequestPhaseSeconds, 8567.0107, metrics.Decode)
			at.Want(t, s, &metrics.RequestsRunning, 1)
			at.Want(t, s, &metrics.SpecDraftTokens, 532754)
			at.Want(t, s, &metrics.SpecAcceptedTokens, 354764)

			ttft, ok := at.Find(s, &metrics.TimeToFirstToken)
			if !ok || ttft.Hist == nil {
				t.Fatal("time to first token: absent")
			}
			if ttft.Hist.Count != 1049 || ttft.Hist.Sum != 1276.910841 {
				t.Errorf("time to first token: count %d sum %v, want 1049 and 1276.910841", ttft.Hist.Count, ttft.Hist.Sum)
			}
			for le, want := range map[float64]uint64{0.25: 23, 1: 764, 5: 996, 10: 1049} {
				if got := ttft.Hist.Buckets[le]; got != want {
					t.Errorf("time to first token le=%v: %d, want %d", le, got, want)
				}
			}

			// prompt_tokens_total (40,840,385) includes the 39,296,704 cached
			// tokens; prefill tokens are never derived by subtraction.
			if _, ok := at.Find(s, &metrics.Tokens, metrics.Prefill); ok {
				t.Error("prefill tokens emitted: TensorFold has no computed-token counter")
			}
			// kv_cache_usage_ratio is one stream's context fill, not pool
			// usage; health requests_total has no finish reason; rounds_total
			// may include rounds that drafted nothing.
			at.Absent(t, s, &metrics.KVCacheUsage)
			at.Absent(t, s, &metrics.Requests)
			at.Absent(t, s, &metrics.EnginePhaseSeconds)
			at.Absent(t, s, &metrics.SpecVerifySteps)
		})
	}
}

// The server labels no series with a model, so any served_model passes:
// discovery takes identity from /v1/models instead.
func TestNativeUnlabeled(t *testing.T) {
	if _, err := MapNative(at.LoadText(t, native), "other"); err != nil {
		t.Fatalf("unlabeled body refused: %v", err)
	}
}

// Without the recipe's tensorfold_health:* families, the upstream series
// still map, and the health-only ones stay absent (not measured).
func TestNativeWithoutHealth(t *testing.T) {
	fams := at.LoadText(t, native)
	for name := range fams {
		if strings.HasPrefix(name, "tensorfold_health:") {
			delete(fams, name)
		}
	}
	res, err := MapNative(fams, served)
	if err != nil {
		t.Fatal(err)
	}
	at.Valid(t, res.Samples)
	at.Want(t, res.Samples, &metrics.Tokens, 504646, metrics.Decode)
	at.Want(t, res.Samples, &metrics.SpecAcceptedTokens, 354764)
	at.Absent(t, res.Samples, &metrics.PromptCachedTokens)
	at.Absent(t, res.Samples, &metrics.RequestPhaseSeconds)
}

// Each mapping refuses the other exposition, and Map refuses a body that
// carries both.
func TestExpositionsDoNotCross(t *testing.T) {
	if _, err := MapNative(at.LoadText(t, live), served); err == nil {
		t.Error("MapNative mapped the patch 0150 body")
	}
	if _, err := MapPatch0150(at.LoadText(t, native), served); err == nil {
		t.Error("MapPatch0150 mapped the native body")
	}
	mixed := at.LoadText(t, native)
	for name, fam := range at.LoadText(t, live) {
		mixed[name] = fam
	}
	if _, err := Map(mixed, served); err == nil {
		t.Error("Map accepted a body with both expositions")
	}
	if IsNative(at.LoadText(t, live)) || IsPatch0150(at.LoadText(t, native)) {
		t.Error("a signature matched the other exposition")
	}
	if !IsNative(at.LoadText(t, native)) || !IsPatch0150(at.LoadText(t, live)) {
		t.Error("a signature missed its own exposition")
	}
}
