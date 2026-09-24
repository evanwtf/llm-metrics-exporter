# Compose engine settings

On Linux, Compose automatically loads `.env` next to `compose.yaml` when run
from this repository. Copy `.env.example` and edit it locally; neither `export`
nor `source .env` is needed. Shell variables with the same names override `.env`,
so remove stale exports if your edits appear ineffective. Use `docker compose
--env-file <private-file> ...` to select another file explicitly. Keep local
settings private; `.env` is project-ignored. Do not paste expanded Compose
configuration into public logs or issues.

## Register and start

Set `LLM_ENGINE`, `LLM_ENGINE_ENDPOINT` (base URL), `LLM_ENGINE_MODEL` (exported
identity), and preferably `LLM_ENGINE_SERVED_MODEL` (exact upstream name).
The template also exposes backend, nodes, run ID, issue, log and trace paths.
Defaults are `vllm`, `local-model`, one node, run ID `static`, and issue 0.
Empty/invalid required values fail CLI validation; no engine is guessed.
The shipped endpoint/model placeholders must be replaced.

Set `LLM_EXPORTER_UID` and `LLM_EXPORTER_GID` to the output of `id -u` and
`id -g` for the user that owns the registrations directory. The default 1000:1000
is merely a common Linux account ID, not automatic detection. The one-shot
writer uses those IDs; the long-running exporter still uses the image's nonroot
user. Its mount stays read-only and needs read/traverse access to the files.

From the repository root, with the default registration location:

```sh
mkdir -p "$HOME/.local/state/llm-metrics-exporter/registrations"
docker compose run --rm --build register
docker compose up -d --build exporter
```

For a custom `LLM_EXPORTER_REGISTRATION_DIR`, create that exact host directory
instead. Compose's default does not follow `XDG_STATE_HOME`; set the override
explicitly if native launchers use a different state directory. Both services
use the same setting and refuse to silently create a root-owned bind directory.
Registration is atomic and does not contact or launch the model server. No
host Go installation is required. Invalid settings or unwritable directories
make the registration command fail; do not proceed until it succeeds.

The `register` service has the `setup` profile, so ordinary `up` does not rerun
it. An explicit `run register` activates it automatically. Do not enable the
setup profile persistently. Existing launcher/static-YAML workflows still work
without an `.env` engine definition or a registration setup run.
Automatic discovery (`LLM_EXPORTER_DISCOVERY=local`, the default) needs none
of these engine settings; a pin takes precedence over a discovered target on
the same endpoint. Set `LLM_EXPORTER_DISCOVERY=off` for pins only.

Set a stable, unique `LLM_EXPORTER_HOST`; host networking does not give a
container the host's hostname. The listener defaults to `:9109` without TLS or
authentication. Use `LLM_EXPORTER_LISTEN=127.0.0.1:9109` for local-only access;
keep port 9109 unless you also override the image healthcheck.

## Apply changes and remove targets

After changing engine settings, rerun `docker compose run --rm --build register`.
The same backend replaces its registration; other backends are untouched.
Collectors pick up changes on the next collection. Editing `.env` or restarting
the exporter alone does not rewrite registrations. Changes to the exporter's
own environment/listener require `docker compose up -d exporter` to recreate it;
`restart` does not apply a changed environment.

To remove a target, override the helper's default command using its original
backend and run ID (replace the example if changed):

```sh
docker compose run --rm register deregister --registration-dir /registrations \
  --backend local-model --run-id static
```

This cannot delete a newer registration owned by a different run ID. Changing
the backend in `.env` does not delete the old backend; explicitly deregister
the old one if retiring it. One `.env` defines one setup target at a time;
multiple engines can use separate private env files or normal registrations.
`docker compose stop exporter` stops only the observer, not the engine, and
leaves registrations intact. With discovery enabled, a deregistered local
engine becomes discoverable again; see [automatic discovery](discovery.md).

For ds4, mount its log directory into the exporter at the same absolute path
as `LLM_ENGINE_LOG_PATH` (see the comments in `compose.yaml`). Path flags do not
create mounts. MTPLX's trace setting is reserved; its adapter remains planned.
For remote write, see [the sender/service guide](remote-write.md); these engine
settings do not configure receiver credentials.

## Previous manual setup

The original Docker quickstart used shell exports and a writable one-shot
exporter mount. That workflow remains available for ad hoc invocations:

```sh
export LLM_EXPORTER_REGISTRATION_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/llm-metrics-exporter/registrations"
export LLM_EXPORTER_HOST='inference-example'
export LLM_EXPORTER_LISTEN='127.0.0.1:9109'
mkdir -p "$LLM_EXPORTER_REGISTRATION_DIR"
docker compose build exporter
docker compose run --rm --no-deps --user "$(id -u):$(id -g)" \
  --volume "$LLM_EXPORTER_REGISTRATION_DIR:/registrations:rw" \
  exporter register --registration-dir /registrations \
  --backend quickstart --engine vllm --endpoint "$ENGINE_URL" \
  --model "$SERVED_MODEL" --served-model "$SERVED_MODEL" --nodes "$NODES" \
  --run-id quickstart
docker compose up -d --build exporter
docker compose ps exporter
```

This older example assumes `ENGINE_URL`, `SERVED_MODEL` and `NODES` were set
as in the native quickstart, and subsequent commands use the same shell.
Its initial image build supplies the registration binary; `up --build` reuses
the cache. Cleanup can use the same `run` prefix with `exporter deregister
--registration-dir /registrations --backend quickstart --run-id quickstart`.
Prefer `.env` plus the setup service for repeatable deployment.
