# Working on Browser Platform

## Scope and design

- Read the relevant [ADRs](docs/adrs/) before changing behavior. Proposed endpoints are not implemented features.
- Keep this service focused on partner, client and site records, staff permissions, instance assignments and identity setup. Extension sessions, policies and telemetry belong to the instance backend.
- Build only the requested scope. Prefer direct code and existing libraries; add abstractions, configuration options or dependencies only for a current need. Preserve useful operational safeguards when simplifying.
- Ask about changes to ownership, access rules or service responsibilities. Resolve routine implementation details using the existing design.
- Keep documentation concise and factual. Use Mermaid for diagrams and leave Markdown prose unwrapped. Update the README when implemented behavior changes.

## Go

- Use idiomatic names and small, purposeful packages. Keep helpers private to their package until another package needs them.
- Group imports as standard library, third-party and local. Format with `gofmt` or `goimports`.
- Prefer concrete types. Define small interfaces at the consumer when they provide a useful boundary; avoid generic service or repository frameworks.
- Return errors with useful context and `%w`. Log an error at the boundary that handles it, rather than both logging and returning it.
- Pass request contexts into database calls. Bound external work and give every goroutine a shutdown path. Close acquired resources.
- Do not log credentials, tokens, connection strings or sensitive request bodies. Return safe errors to callers.
- Test observable behavior, especially failures and isolation boundaries. Use standard Go tests and the race detector; avoid tests that simply repeat the implementation. Profile before optimizing.

## Data and identity

- PostgreSQL is Platform's store. Use explicit SQL columns, parameterized queries, transactions and database constraints where correctness requires them.
- Preserve parent-scoped identity: a client needs `partner_id` and `client_id`; a site also needs `site_id`. IDs are allocated from `1000` through `9999` and are not reused within a parent.
- Identify people by registered realm/issuer and permanent user ID. Email is not an account identifier or permission check; its domain is a console discovery hint only.
- Use `*_ts_utc` names for stored and returned timestamps, PostgreSQL `timestamptz`, and RFC 3339 UTC values in JSON.
- Keep one authoritative representation of each stored record. Use separate request or response types when callers must not set or see stored fields. JSON names use `snake_case`.
- Add new embedded SQL migrations; do not rewrite published migrations. Keep migration history in `public.goose_db_version` and retain locking for concurrent startup.
- Integration tests use a new disposable database. Never clear application data to make a test pass.

## Configuration and deployment

- The Go process reads environment variables only. Helm supplies non-secret settings and Secrets; do not add application YAML loading or Kubernetes API clients for configuration.
- Local private Helm values create separate PostgreSQL and Keycloak Secrets. Production uses `postgres.existingSecret` and `keycloak.existingSecret` with the same environment-variable contract. Keep private values under ignored `.local/`; never commit credentials.
- Use the install/uninstall scripts and local `kind-mutexai-dev` cluster. Pass an explicit Kubernetes context. Do not use AKS or restart other clusters as part of normal local work.
- PostgreSQL runs in Kubernetes for local development. Do not add a standalone Docker or Compose workflow.
- Preserve the private ClusterIP service, restricted ingress, container security settings, health probes and bounded shutdown. Do not add public routes or a generic console proxy.
- Current operator-only startup has no application authentication. Future console management requires the caller and staff access checks described by the ADRs; network access alone does not authorize an operation.

## Verification

- Use `GOWORK=off` for direct Go commands; Make already sets it.
- Run checks appropriate to the change. `make check` covers formatting, vet, lint, race tests and Helm checks. `make build` builds the binary.
- For migration changes, run `make integration` against a new disposable PostgreSQL database in local Kubernetes. For deployment changes, verify the affected behavior using `make install`.
- Preserve unrelated work in this repository and sibling repositories. Report what changed, what was tested and any remaining limitation.

These rules adapt the relevant Go, design-document and data-model guidance from the control-plane repository's `.claude/rules`. Browser-specific authentication, ClickHouse conventions and speculative performance rules do not apply here.
