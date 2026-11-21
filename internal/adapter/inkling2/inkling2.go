// Package inkling2 reads the Inkling 2 server of oracle9i88/inkling2-exl3
// (server/i2_server.py), an exllamav3 overlay. The server has no /metrics.
// GET /health returns JSON with the scheduler's lifetime counters, and
// GET /v1/models names the one served model. docs/adapters.md, "Inkling 2",
// records the source and interval of every series, and why the others stay
// unavailable.
package inkling2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/evanwtf/llm-metrics-exporter/internal/adapter"
	"github.com/evanwtf/llm-metrics-exporter/internal/metrics"
)

// Engine is the label value. It names this server's API, not stock
// exllamav3, which serves a different API.
const Engine = "inkling2-exl3"

// Info registers the adapter.
var Info = adapter.Info{
	Engine:  Engine,
	Version: "1",
	New:     func(c adapter.Config) adapter.Adapter { return &Adapter{cfg: c} },
}

// ErrNotInkling means a /health body does not have the Inkling 2 signature.
var ErrNotInkling = errors.New("not an Inkling 2 /health body")

// ErrMixedSteps means the server is a version with mixed prefill-and-decode
// steps (the main line at d982916, i2_server.py lines 458 and 662-664). Those
// steps count into mix_pre_tok and mix_tok, not pre_tok and dec_tok, so this
// mapping would silently lose tokens. Such a server is refused.
var ErrMixedSteps = errors.New("inkling2: /health has mix_* counters: mixed steps are not supported")

// Stats is the STATS dict (i2_server.py at 299eeaf3, lines 436-437; the
// prefix_* keys from i2_prefix_cache.py lines 56-57; trims at line 534).
// A pointer is nil when the key is absent.
type Stats struct {
	DecSteps   *float64 `json:"dec_steps"`
	DecTime    *float64 `json:"dec_time"`
	DecRows    *float64 `json:"dec_rows"`
	DecTok     *float64 `json:"dec_tok"`
	PreTime    *float64 `json:"pre_time"`
	PreTok     *float64 `json:"pre_tok"`
	SpecSteps  *float64 `json:"spec_steps"`
	PlainSteps *float64 `json:"plain_steps"`
	// PrefixHitTok is the prompt tokens restored from the prefix cache.
	PrefixHitTok *float64 `json:"prefix_hit_tok"`
}

// Prefix is pcache.info() (i2_prefix_cache.py lines 139-141).
type Prefix struct {
	Entries   *float64 `json:"entries"`
	FreePages *float64 `json:"free_pages"`
}

// Health is the /health body (i2_server.py lines 743-745).
type Health struct {
	Status   string   `json:"status"`
	Stats    *Stats   `json:"stats"`
	Spec     *bool    `json:"spec"`
	KVTokens *float64 `json:"kv_tokens"`
	Active   *float64 `json:"active"`
	Queued   *float64 `json:"queued"`
	Prefix   *Prefix  `json:"prefix"`
}

// Parse decodes a /health body and checks its signature: status "ok", the
// scheduler counters, spec, kv_tokens, active, queued and the prefix-cache
// info. A body without all of them is ErrNotInkling, so another engine's
// /health (llama.cpp's {"status":"ok"}, say) is never read as this one.
func Parse(body []byte) (Health, error) {
	var h Health
	if err := json.Unmarshal(body, &h); err != nil {
		return Health{}, fmt.Errorf("%w: %v", ErrNotInkling, err)
	}
	var raw struct {
		Stats map[string]json.RawMessage `json:"stats"`
	}
	if json.Unmarshal(body, &raw) == nil && h.Status == "ok" {
		for k := range raw.Stats {
			if strings.HasPrefix(k, "mix_") {
				return Health{}, ErrMixedSteps
			}
		}
	}
	if h.Status != "ok" || h.Stats == nil || h.Spec == nil || h.KVTokens == nil ||
		h.Active == nil || h.Queued == nil || h.Prefix == nil || h.Prefix.Entries == nil || h.Prefix.FreePages == nil {
		return Health{}, ErrNotInkling
	}
	s := h.Stats
	for _, v := range []*float64{s.DecSteps, s.DecTime, s.DecRows, s.DecTok, s.PreTime, s.PreTok, s.SpecSteps, s.PlainSteps} {
		if v == nil {
			return Health{}, ErrNotInkling
		}
	}
	return h, nil
}

// Map turns /health into canonical samples. Only the three token counters
// have the schema's meaning; every other series stays absent (unavailable),
// never zero.
func Map(h Health) adapter.Result {
	var out []metrics.Sample
	add := func(def *metrics.Def, v *float64, labels ...string) {
		if v != nil {
			out = append(out, metrics.Sample{Def: def, Labels: labels, Value: *v})
		}
	}
	// pre_tok: prompt tokens each prefill chunk computed (line 587). A
	// restored prefix starts the prefill at the restore point (line 522), so
	// cache hits are not in it.
	add(&metrics.Tokens, h.Stats.PreTok, metrics.Prefill)
	// dec_tok: tokens the decode steps produced, accepted drafts plus one
	// target token per row and step (line 632). The first token of each
	// request comes from its last prefill chunk and is not in it.
	add(&metrics.Tokens, h.Stats.DecTok, metrics.Decode)
	// prefix_hit_tok: prompt tokens restored from a prefix snapshot at
	// admission (i2_prefix_cache.py line 116).
	add(&metrics.PromptCachedTokens, h.Stats.PrefixHitTok)
	return adapter.Result{Samples: out}
}

// Fetch GETs /health and parses it.
func Fetch(ctx context.Context, cfg adapter.Config) (Health, error) {
	body, err := adapter.FetchBody(ctx, cfg.Client, cfg.Endpoint, "/health", cfg.MaxBody)
	if err != nil {
		return Health{}, err
	}
	return Parse(body)
}

// IsModels reports whether a /v1/models body has this server's shape
// (i2_server.py lines 766-768): exactly one model, owned_by "local" and a
// numeric max_model_len. It returns the model id. vLLM, SGLang and llama.cpp
// report other owned_by values, so discovery never sends them GET /health,
// which on SGLang runs a generation.
func IsModels(body []byte) (string, bool) {
	var data struct {
		Data []struct {
			ID          string          `json:"id"`
			OwnedBy     string          `json:"owned_by"`
			MaxModelLen json.RawMessage `json:"max_model_len"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &data) != nil || len(data.Data) != 1 {
		return "", false
	}
	d := data.Data[0]
	var n float64
	if d.ID == "" || d.OwnedBy != "local" || json.Unmarshal(d.MaxModelLen, &n) != nil {
		return "", false
	}
	return d.ID, true
}

// Models GETs /v1/models and returns the served ids.
func Models(ctx context.Context, cfg adapter.Config) ([]string, error) {
	body, err := adapter.FetchBody(ctx, cfg.Client, cfg.Endpoint, "/v1/models", cfg.MaxBody)
	if err != nil {
		return nil, err
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("inkling2: /v1/models: %w", err)
	}
	ids := make([]string, 0, len(data.Data))
	for _, d := range data.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// Adapter is a pull adapter over /health. With served_model set, it also
// reads /v1/models each collection and requires that name there: /health
// names no model.
type Adapter struct{ cfg adapter.Config }

// Start does nothing: the adapter keeps no state.
func (a *Adapter) Start(context.Context) error { return nil }

// Collect reads /health and, with served_model, validates it.
func (a *Adapter) Collect(ctx context.Context) (adapter.Result, error) {
	h, err := Fetch(ctx, a.cfg)
	if err != nil {
		return adapter.Result{}, err
	}
	if a.cfg.ServedModel != "" {
		ids, err := Models(ctx, a.cfg)
		if err != nil {
			return adapter.Result{}, err
		}
		if !slices.Contains(ids, a.cfg.ServedModel) {
			return adapter.Result{}, &adapter.MismatchError{Want: a.cfg.ServedModel, Reported: ids}
		}
	}
	return Map(h), nil
}

// Close does nothing.
func (a *Adapter) Close() error { return nil }
