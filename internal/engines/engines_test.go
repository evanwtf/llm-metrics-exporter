package engines

import (
	"slices"
	"testing"

	"github.com/evanwtf/llm-metrics-exporter/internal/registration"
)

func TestEveryAdapterIsARegistrableEngine(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range All() {
		if !slices.Contains(registration.Engines, a.Engine) {
			t.Errorf("%s: not in registration.Engines", a.Engine)
		}
		if seen[a.Engine] {
			t.Errorf("%s: two adapters", a.Engine)
		}
		seen[a.Engine] = true
		if a.Version == "" || a.New == nil {
			t.Errorf("%s: incomplete Info", a.Engine)
		}
	}
}
