// Package version holds the exporter's version. It is declared here and
// nowhere else; the release workflow refuses a tag that disagrees with it
// (scripts/release).
package version

// Version is the release version, without the leading "v".
const Version = "0.1.0"

// Revision is the git commit, set at build time with
// -ldflags "-X github.com/evanwtf/llm-metrics-exporter/internal/version.Revision=<sha>".
var Revision = "unknown"
