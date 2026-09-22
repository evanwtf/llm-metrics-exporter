// Package release holds the checks the release workflow runs before it
// publishes anything. Both refuse rather than guess.
package release

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// Zip writes a new zip at out with each file under dir/, keeping its mode
// so the binary stays executable after unzip. It refuses to overwrite out,
// a missing input, and a dir that is not a plain name.
func Zip(out, dir string, files ...string) (err error) {
	if len(files) == 0 {
		return errors.New("no files to zip")
	}
	if dir == "" || strings.ContainsAny(dir, `/\`) || dir == "." || dir == ".." {
		return fmt.Errorf("directory name %q must be a plain name", dir)
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out)
		}
	}()
	zw := zip.NewWriter(f)
	for _, path := range files {
		if err := add(zw, dir, path); err != nil {
			return err
		}
	}
	return zw.Close()
}

func add(zw *zip.Writer, dir, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	h, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	h.Name = dir + "/" + filepath.Base(path)
	h.Method = zip.Deflate
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(w, src)
	return err
}
