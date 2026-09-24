package compose_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineEnv(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose not installed")
	}
	body, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, custom := range []bool{false, true} {
		name := "defaults"
		if custom {
			name = "dotenv"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			env := ""
			if custom {
				env = "LLM_ENGINE=llamacpp\nLLM_ENGINE_ENDPOINT=http://127.0.0.1:8080\nLLM_ENGINE_MODEL='org/demo-model'\nLLM_ENGINE_SERVED_MODEL=upstream-model\nLLM_ENGINE_BACKEND=example\nLLM_ENGINE_NODES=2\nLLM_ENGINE_RUN_ID=example-run\nLLM_ENGINE_ISSUE=42\nLLM_EXPORTER_UID=1234\nLLM_EXPORTER_GID=2345\nLLM_EXPORTER_REGISTRATION_DIR=" + filepath.ToSlash(filepath.Join(dir, "registrations")) + "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("docker", "compose", "--profile", "setup", "config", "--format", "json")
			cmd.Dir = dir
			// Never load the operator's .env or shell overrides into fixtures.
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "LLM_") && !strings.HasPrefix(entry, "COMPOSE_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("compose config failed: %v", err)
			}
			type volume struct {
				Source, Target string
				ReadOnly       bool `json:"read_only"`
				Bind           struct {
					CreateHostPath bool `json:"create_host_path"`
				}
			}
			var config struct {
				Services map[string]struct {
					Command  []string
					Profiles []string
					User     string
					ReadOnly bool `json:"read_only"`
					Volumes  []volume
				}
			}
			if err := json.Unmarshal(out, &config); err != nil {
				t.Fatal(err)
			}
			setup, exporter := config.Services["register"], config.Services["exporter"]
			if len(setup.Profiles) != 1 || setup.Profiles[0] != "setup" || len(exporter.Profiles) != 0 {
				t.Fatal("registration must be opt-in, exporter must remain enabled")
			}
			if !setup.ReadOnly || !exporter.ReadOnly || len(setup.Volumes) != 1 || len(exporter.Volumes) != 1 {
				t.Fatal("unexpected filesystem configuration")
			}
			writer, reader := setup.Volumes[0], exporter.Volumes[0]
			if writer.Source != reader.Source || writer.Target != "/registrations" || reader.Target != writer.Target || writer.ReadOnly || !reader.ReadOnly || writer.Bind.CreateHostPath || reader.Bind.CreateHostPath {
				t.Fatal("writer and reader must share an existing directory with distinct mount permissions")
			}
			want := []string{"register", "--registration-dir=/registrations", "--engine=vllm", "--endpoint=", "--model=", "--backend=local-model", "--nodes=1", "--run-id=static"}
			user := "1000:1000"
			if custom {
				want = []string{"--engine=llamacpp", "--endpoint=http://127.0.0.1:8080", "--model=org/demo-model", "--served-model=upstream-model", "--backend=example", "--nodes=2", "--run-id=example-run", "--issue=42"}
				user = "1234:2345"
			}
			if setup.User != user {
				t.Errorf("user = %q; want %q", setup.User, user)
			}
			for _, arg := range want {
				found := false
				for _, got := range setup.Command {
					found = found || got == arg
				}
				if !found {
					t.Errorf("missing argument %q in synthetic configuration: %q", arg, setup.Command)
				}
			}
		})
	}
}
