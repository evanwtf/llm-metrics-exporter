# Public configuration and credentials

The [root rules](../AGENTS.md#public-repository-and-secrets) apply to all work.
This project does not establish a particular vault or provision credentials.
Prefer an existing maintained secret manager; the operator chooses the manager
when none is established. Do not require a vendor or subscription.

## Actual credential interface

The built-in remote-write sender accepts `--remote-write-token-file
<private-token-file>` and reads that file for each request, permitting rotation
without restarting. Use the chosen manager's supported runtime integration to
make a protected file available to the exporter. Keep it outside this checkout
unless there is an explicit, narrowly scoped project ignore rule for its chosen
path. Do not put a literal token in command arguments, shell history, environment
examples, debug output or a receiver URL. Do not fetch real secrets to verify
documentation. The receiver/proxy must independently accept the configured
authentication; enabling its receiver does not add authentication.

See [remote-write authentication](remote-write.md#built-in-sender) for TLS,
URL restrictions and token-file behavior. This interface does not authenticate
engine scrapes or incoming exporter HTTP requests. Runtime metrics/state and
logs can contain private deployment identity even when they contain no tokens;
keep them private and sanitize captures before sharing.

## Local files and publication checks

The project [`.gitignore`](../.gitignore) explicitly ignores `.env`,
`*.local.yml` (including the documented `prometheus-agent.local.yml`), and
`/.public-denylist`. Keep [`.env.example`](../.env.example) trackable and
sanitized, with placeholders only. No fixed token-file path is defined by the
project. If a new local credential path is introduced, add its narrow ignore
rule to this repository and include that rule in Git delivery; global ignores
or `.git/info/exclude` alone are insufficient. Do not use broad rules that hide
source or public certificates.

`scripts/check-public.sh` detects private address ranges, private DNS suffixes
and home-directory paths. Put ordinary-looking private names in the ignored
`.public-denylist`. The hooks also include private-key detection and gitleaks;
these checks complement, rather than replace, manual review. Never display
secret values while reviewing: inspect filenames, names and metadata, and
redact before any output reaches a transcript. Do not ask a user to paste a
secret into chat.

Check ignore coverage with `git check-ignore --no-index` and tracked status
separately. Ignoring a file neither untracks it nor removes prior exposure.
If exposure is found, report only its path and the required revocation/rotation
follow-up. Do not display the value, rotate/delete credentials or rewrite
history as part of documentation maintenance. Editing current text does not
erase previously published commits.
