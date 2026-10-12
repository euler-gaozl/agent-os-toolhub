# Agent OS ToolHub

Independent Skill/MCP registry and validation service. Owns the `toolhub`
schema: immutable assets/files, activation/health state and audit history.
Registry binds exact versions; ToolHub remains their authority. This service
does not own Agents, execution receipts, credentials or knowledge documents.

## Behavior and boundaries

- Skill ZIP ingestion is bounded and rejects traversal, duplicate/unsafe paths,
  symlinks, executable flags, expansion and credential-like content. Static
  validation is not proof of safe instructions. Package scripts never execute
  on this host. Sources remain reviewable before audited activation.
- MCP discovery/probes use the fixed Streamable HTTP profile, initialize and
  tools/list; health probes do not invoke business tools. Public endpoints need
  HTTPS; private endpoints require an operator-owned exact host:port allowlist.
  Registration, discovery, checks and execution share DNS/IP/redirect policy.
- New resources start disabled. Checks have DB leases/fences, bounded workers,
  timeouts, retries and freshness checks. Tool schema drift blocks resolution.
  The version and activation state are never silently overwritten.
- Invocation crosses HTTP to Runtime's effect/receipt transaction. A lost reply
  does not authorize blindly repeating an external write. Credentials come from
  named Registry resolution; ToolHub cannot read the credential database.

## Independent build and deployment

Requires Go 1.27.2 and PostgreSQL 18. `scripts/Build.ps1` builds the service,
migration and healthcheck with committed vendor, `GOWORK=off`, `GOPROXY=off`.
No sibling repo, private-module token, Node or model key is needed. Linux can
also run `go build -mod=vendor ./cmd/toolhub`. `source-provenance.json` fixes
SDK source/version/checksum; changes require an explicit dependency update.

Run `bin/toolhub --config <protected service.json>`. Use the placeholder fields
in `deploy/service.example.json`: scoped DB connection with
`search_path=toolhub`, distinct token/peer hashes (including self), Registry and
Runtime HTTP endpoints. `TOOLHUB_PROBE_WORKERS` is 1..8 (default 2); HTTP
concurrency defaults to 4 and DB connections to 3. Do not publish internal ports
directly. Configure private network/TLS and per-process/container resource limits.

Provision a non-owner application role. With a separate DDL connection in
`MIGRATION_DATABASE_URL`, run `bin/migrate --role <existing-role>`. Migration
creates/updates only this schema and its grants in one transaction, including
the audit sequence; locks/statements have deadlines. The normal service does
not do DDL, bootstrap secrets or read another service's tables. Existing imports
are explicit operator operations, never an automatic startup data copy.

Upgrade or roll back only this process under `deploy/release-contract.json`.
Retain DB, immutable versions, audits and configuration. Compatibility across
schema/protocol changes needs a coordinated migration; no destructive down
migration is provided. Integration selects the exact owner commit and owns
Windows activation, not this service's source.

## Verification scope

`go test -mod=vendor -count=1 ./...` and `go vet -mod=vendor ./...`.
`TEST_DATABASE_URL` enables isolated fixture databases/roles, not migration of
the named database. Tests cover registration/activation, probes, schema drift,
monitor lease recovery, scoped role auditing and migration rollback/permissions.
Without PostgreSQL they explicitly skip. CI additionally runs Linux race checks
and builds the Docker image. Fixture MCP tests do not prove every external MCP
is compatible or safe. Whole-platform failures and live independent release
acceptance are separate integration evidence; P2 is still in progress.

Legacy local configurations may omit the Runtime address: this service preserves
the previous loopback default (127.0.0.1:8094) for its bounded heartbeat/client.
Where used, Registry (8096) and ToolHub (8091) defaults are also owned by the
service entrypoint. Explicit environment/config endpoints always take precedence.
Configure these addresses explicitly for containers or multiple hosts. No config
file or shared SDK role policy is rewritten by this compatibility behavior.
