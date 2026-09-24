package registration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaticRegistrationWithoutRunID(t *testing.T) {
	body := []byte("version: 1\nengine: vllm\nendpoint: http://127.0.0.1:1\nmodel: m\nbackend: local\nnodes: 1\n")
	r, err := Parse(body, "local.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if r.RunID != "static" || r.Issue != 0 {
		t.Fatalf("defaults %+v", r)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "local.yaml"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Remove(dir, "local", "static"); err != nil {
		t.Fatal(err)
	}
}
