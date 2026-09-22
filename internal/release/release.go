// Package release holds the checks the release workflow runs before it
// publishes anything. Both refuse rather than guess.
package release

import (
	"fmt"
	"regexp"
	"strings"
)

var semver = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)

// CheckTag refuses a tag that is not exactly "v" + the declared version.
func CheckTag(tag, version string) error {
	m := semver.FindStringSubmatch(tag)
	if m == nil {
		return fmt.Errorf("tag %q is not vMAJOR.MINOR.PATCH", tag)
	}
	if m[1] != version {
		return fmt.Errorf("tag %s but internal/version declares %s", tag, version)
	}
	return nil
}

// Notes returns the body of the "## <version>" section of a changelog. It
// refuses a missing or empty section.
func Notes(changelog, version string) (string, error) {
	lines := strings.Split(changelog, "\n")
	start := -1
	for i, l := range lines {
		if isHeading(l, version) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("docs/changelog.md has no section for %s", version)
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	body := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if body == "" {
		return "", fmt.Errorf("docs/changelog.md section %s is empty", version)
	}
	return body + "\n", nil
}

// isHeading matches "## 0.1.0" and "## 0.1.0 (date)", not "## 0.1.0.1".
func isHeading(line, version string) bool {
	rest, ok := strings.CutPrefix(line, "## "+version)
	return ok && (rest == "" || rest[0] == ' ')
}
