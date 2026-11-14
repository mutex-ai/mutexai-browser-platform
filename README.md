# Mutex Browser Platform

Private Go API for the shared partner and instance directory and global application registry, backed by PostgreSQL. It registers instances, creates a partner with its initial client/site and Keycloak configuration, and stores application definitions with unique host claims. The [identity ADR](docs/adrs/adr-000-identity-and-access.md), [Platform ADR](docs/adrs/adr-001-platform-api.md) and [application registry ADR](docs/adrs/adr-002-application-registry.md) describe the design; their other proposed APIs are not implemented yet.

## Operator API

| Endpoint | Behavior |
| --- | --- |
| `POST /internal/v1/instances` | Register an existing instance's name, console origin and backend origin; return `201`. |
| `GET /internal/v1/instances/{instance_id}` | Read its addresses and derived console client ID and public-key URL. |
| `PATCH /internal/v1/instances/{instance_id}` | Set `accepting_clients`; existing assignments remain. |
| `POST /internal/v1/partners` | Save a partner, initial client/site, instance assignment and setup work; return `202`. |
| `GET /internal/v1/partners/{partner_id}` | Read the initial hierarchy, assigned instance and identity setup status. |
| `GET /internal/v1/applications` | Read the complete global registry, including shared login-provider records. |
| `GET /internal/v1/applications/{app_id}` | Read one application and its edit ETag. |
| `POST /internal/v1/applications` | Create an application with normalized host claims; return `201`, or `200` on a creation replay. |
| `PUT /internal/v1/applications/{app_id}` | Replace an application's editable definition, guarded by `If-Match`; return `200`. |
| `GET /internal/v1/health` | Liveness, independent of PostgreSQL and Keycloak availability. |
| `GET /internal/v1/health/ready` | PostgreSQL readiness with a two-second deadline. |

These are operator endpoints with no application authentication. Use Kubernetes port-forwarding authorized by cluster RBAC. The chart creates a private ClusterIP service, no ingress and an incoming-traffic policy. Console forwarding, authenticated staff access and service TLS must be implemented before console management uses this API.

After starting the port-forward below, register an instance first:

```sh
curl -i http://127.0.0.1:8002/internal/v1/instances \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: instance-example-001' \
  --data '{"name":"Shared instance","console_url":"https://console.example.com","backend_url":"https://browser-api.example.com"}'
```

Use the returned `instance_id` when creating a partner:

```sh
curl -i http://127.0.0.1:8002/internal/v1/partners \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: onboarding-example-msp-001' \
  --data '{"instance_id":1000,"name":"Example MSP","client_name":"Initial client","site_name":"Main office","console_domains":["example-msp.com"]}'
```

One instance can serve clients from many partners. Assignment belongs to the client, and its sites inherit it. The first partner receives `partner_id/client_id/site_id` values `1000/1000/1000`. Later clients and sites retain their full parent keys.

Callers supply approved instance origins, names and console domains. They cannot supply hierarchy IDs, credentials, public keys, arbitrary Keycloak operations or a callback address. Console origins require HTTPS, except HTTP loopback for local development. Backend HTTP supports private local Kubernetes services. Instance addresses are immutable in this version; changing them requires a coordinated workflow for existing login registrations.

Names have a 200-character limit. Domains are normalized, deduplicated and reserved globally; international domains use ASCII punycode. Requests require JSON, reject unknown fields and have a 64 KiB limit.

Retry a timed-out creation with the same `Idempotency-Key` and equivalent input. It returns the same IDs and current status, with `Idempotency-Replayed: true`. Instance retries return `200`; partner retries return `202` until identity setup is complete, then `200`. Conflicting key reuse, another partner's domain or another instance's console origin returns `409`. An unknown instance or one closed to new assignments also returns `409`. Existing creation retries still work after an instance closes to new assignments.

| Setup status | Meaning |
| --- | --- |
| `assignment_required` | An older record has no instance and cannot complete setup. |
| `pending` | Saved and waiting for the worker. |
| `running` | A worker holds the setup lease. |
| `failed` | The last attempt failed; `setup_error` is a safe code and `retry_ts_utc` gives the next attempt time. |
| `complete` | Required Keycloak resources and their permanent references are saved. |

`complete` describes identity setup. It does not mean the UI has deployed its signing key, company SSO is enabled, domain authorization is verified or a human administrator exists. Console domain discovery and instance configuration delivery are not implemented yet.

Timestamp fields use `*_ts_utc` names and RFC 3339 UTC values ending in `Z`. Record creation and update times come from Go's `time.Now().UTC()`. PostgreSQL stores them as `timestamptz`; application connections use UTC. Worker scheduling and lease comparisons use the database clock so replicas agree on deadlines.

Partner and instance IDs use `1000–9999`. Client numbers are scoped to their partner, and site numbers to their client. Failed transactions roll back allocation. Committed IDs are never reused; exhaustion returns `id_capacity_exhausted`. Successful creation retries still work after exhaustion.

## Application registry

Operators create and edit global app definitions through the port-forwarded API. PostgreSQL is authoritative; application startup never imports or overwrites registry data. This change does not move the existing CP catalog or implement CP cache integration. Those consumers remain unchanged until they are explicitly switched over.

POST and PUT take the existing catalog-v3 definition without server-owned `id`, `meta.revision` or `meta.updated_at`. Omitted `meta` defaults to `{"schema_version":3}`. Go validates the complete definition against [the registry schema](src/storage/applications/definition.schema.json) before storage. GET responses wrap the full app under `app`, with `created_ts_utc` and `updated_ts_utc` alongside it. The app includes its permanent ID and server-managed `meta.revision`.

```sh
curl -i http://127.0.0.1:8002/internal/v1/applications \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: application-example-001' \
  --data-binary @docs/examples/application.json

curl -i http://127.0.0.1:8002/internal/v1/applications/APP_ID

# Edit the input file, then use the exact ETag from GET (including quotes).
curl -i -X PUT http://127.0.0.1:8002/internal/v1/applications/APP_ID \
  -H 'Content-Type: application/json' \
  -H 'If-Match: "APP_ID:1"' \
  --data-binary @docs/examples/application.json
```

App IDs are 12 lowercase hexadecimal characters, independent of the four-digit customer hierarchy. Slugs and hostnames are trimmed and lowercased; IDNs become ASCII punycode, one trailing dot is removed, and exact resolver hosts omit `www.` to match CP's existing alias behavior. Host lists are sorted and deduplicated. Homepage order is retained. Wildcards such as `*.example.com` and `*-my.sharepoint.com` remain intact; Unicode labels containing a wildcard must be supplied as ASCII. Bare DNS names are required; URLs, ports, IP addresses and malformed DNS names are rejected.

PostgreSQL owns one canonical host-pattern claim per app. A conflicting normalized pattern or slug returns `409`; a failed write leaves the previous definition, hosts, revision and timestamps intact. Distinct overlapping patterns are not identical claims: exact-over-wildcard matching remains a CP responsibility. Dependencies must refer to existing apps other than the edited app. There is no delete or partial-update route.

GET collection returns `{"items":[...],"count":N}` in app-ID order, without pagination or filtering so CP can obtain the complete registry. GET and writes return a strong ETag for the selected app. PUT requires that exact tag: missing `If-Match` returns `428`, an outdated revision returns `412`, and malformed tags return `400`. Retrying POST with the same key and normalized input returns the same app's current record, even after an edit. Database failures return `503`, never an apparently successful empty registry.

## Keycloak setup

Platform uses one service-account client, `master/platform-provisioner`, to administer identity resources. Grant `create-realm` and ensure its token scope includes permissions automatically granted for new realms. Existing realms require explicit grants. Platform never uses a human administrator password or shares its administration credential with the UI.

For partner `1000` assigned to instance `1000`, the worker creates:

| Realm | Resource | Configuration |
| --- | --- | --- |
| `1000` | `platform-ui-1000` | Confidential console login using a signed client assertion and code flow with PKCE S256. Password, implicit and service-account grants are disabled. |
| `1000` | `platform-api` | API audience and client roles `admin`, `operator`, `viewer`; login flows disabled. |
| `1000-sso` | `browser-extension` | Public enrollment login with PKCE S256 and no console roles. |
| `1000-sso` | Organization alias `1000` | Initial client; permanent organization ID included by the membership mapper. |

Realms remain partner-specific. The console client ID is instance-specific and repeats in each partner realm that instance serves. A second partner on instance `1000` gets another `platform-ui-1000` registration in its own realm. A different instance gets its own client ID and public-key source. This does not create extra UI deployments.

Each resource is stamped with the saved setup identity and partner. Existing resources are verified before reuse. Changed ownership, callback addresses, login settings, authenticator or public-key source fail safely; retries do not overwrite them. Unrelated client registrations are left untouched. Realm keys and resource IDs are generated by Keycloak.

The Console realm is enabled after configuration. The Client SSO realm and initial organization remain disabled until enrollment setup is implemented. No human accounts are created. An operator manually creates the first Console-realm account, assigns `platform-api`'s `admin` role and sets a temporary password. Local passwords require at least 15 characters using Keycloak's built-in policy. MFA is optional initially. Console clients receive only `profile`, `email` and `roles` scopes; optional scopes, including offline access, are disabled.

### Console signing contract

Each UI instance holds one RSA signing key in its own deployment Secret. Platform never receives or stores that private key. It registers the public-key endpoint derived from the approved console origin.

| Setting | Value for partner `1000`, instance `1000` |
| --- | --- |
| Client ID | `platform-ui-1000` |
| Client authenticator | `client-jwt` (`private_key_jwt` in OIDC) |
| Public keys | `{console_url}/.well-known/jwks.json` |
| Assertion signing algorithm | `RS256` |
| Maximum assertion lifetime | 60 seconds |
| Login callback | `{console_url}/api/auth/callback/keycloak-1000` |
| Logout redirect | `{console_url}/` |

The UI must publish only public keys in JWKS format, with a stable `kid` for each key. It must sign a fresh assertion with a unique `jti`, the client ID as `iss` and `sub`, and an audience accepted by the selected realm. Login, refresh and authenticated logout must all use the selected realm and the same instance signing identity. The Keycloak token endpoint URL is the assertion audience used by the integration checks. Keep PKCE and bind the selected issuer to the login attempt.

For rotation, publish the new public key alongside the old one before signing with the new key. Retain the old public key during the transition and allow for Keycloak's key cache. Removing a public key does not revoke previously issued user tokens or sessions. Each instance uses its own key pair.

Keycloak must be able to reach the registered public-key URL. A loopback console origin supports host development but is not reachable as the host UI from a Keycloak pod; use a properly routed development hostname for that integration. Platform does not fetch arbitrary key URLs or add Kubernetes deployment permissions.

The current UI still uses a single realm and a client secret. Its key publication, signed assertions, multi-realm login and discovery wiring are a separate change. This repository configures the Keycloak side only. [Keycloak documents signed client assertions and JWKS-based key rotation](https://www.keycloak.org/docs/latest/server_admin/index.html#_signed_jwt).

## Configuration and local Kubernetes

The Go process reads environment variables only. Helm supplies settings and uses `envFrom.secretRef` for credentials; the process does not read Kubernetes Secrets or YAML itself.

| Process setting | Helm setting |
| --- | --- |
| `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DATABASE`, `POSTGRES_USERNAME`, `POSTGRES_SSLMODE` | `postgres.host`, `port`, `database`, `username`, `sslMode` |
| `POSTGRES_PASSWORD` | `postgres.existingSecret`; private `postgres.password` for chart-created local Secrets |
| `KEYCLOAK_URL`, `KEYCLOAK_CLIENT_ID` | `keycloak.url`, `clientId` |
| `KEYCLOAK_CLIENT_SECRET` | `keycloak.existingSecret`; private `keycloak.clientSecret` for chart-created local Secrets |
| `LOG_LEVEL` and `HTTP_*_TIMEOUT_SEC` | `logLevel` and `server.*TimeoutSec` |

PostgreSQL host, port, database, username and TLS mode are connection settings; they remain configurable outside the password Secret. Production references an existing Secret under each dependency: `postgres.existingSecret` must contain `POSTGRES_PASSWORD`, and `keycloak.existingSecret` must contain `KEYCLOAK_CLIENT_SECRET`. Both Secrets must be in the application namespace. No password is supplied in committed values.

Each dependency can independently use an external Secret or a chart-created one. Local private values let the chart create `browser-platform-postgres` and `browser-platform-keycloak` for the default release name. Chart-managed credential changes trigger a rollout; externally managed credential rotation requires a rollout restart. Platform needs no UI credential. Helm rejects unknown settings and validates connection settings, types and timeout ranges before rendering.

Use the shared local `kind-mutexai-dev` cluster and its Kubernetes PostgreSQL and Keycloak services. Prerequisites are Kind, kubectl, Helm, the container engine used by Kind, a dedicated Platform database/user and the configured provisioning service account.

```sh
mkdir -p .local
cp charts/browser-platform/values-local.example.yaml .local/values.yaml
chmod 600 .local/values.yaml
# Fill the database password and provisioning client secret privately.
make install
kubectl --context kind-mutexai-dev -n mutexai-platform \
  port-forward deployment/browser-platform 8002:8080
```

`.local/` is excluded from Git and Docker builds. Install scripts use an explicit context and do not change the current context. They build and load a unique image into Kind. Override `PLATFORM_VALUES_FILE`, `PLATFORM_CLUSTER_NAME` or `PLATFORM_NAMESPACE` when needed. There is no standalone PostgreSQL container or Compose workflow.

`make deploy` aliases `make install`. `make uninstall` retains the database, namespace, private values and external Secrets. `.env.example` supports host debugging through port-forwards; the process does not load it automatically.

## Reliability and verification

One PostgreSQL transaction saves the initial hierarchy, assignment, counters, domain reservations, setup task and change event. Operator API and worker events identify their source; individual operator identity requires configured Kubernetes auditing or future application authentication.

A worker in the application claims one task at a time. PostgreSQL coordinates replicas with expiring, fenced leases; no transaction stays open during Keycloak calls. Claim queries have a five-second deadline. Attempts have a 45-second deadline inside a 60-second lease. Failures retry after five seconds with backoff capped at five minutes. Expired workers cannot complete or fail a replacement attempt. Unassigned older records are not processed. There is no separate assignment API.

The shared PostgreSQL pool allows 10 open and five idle connections, a 30-minute lifetime and five-minute idle limit. HTTP and worker work stop before clients and the pool close. API database work has a five-second deadline. Keycloak HTTP requests have a ten-second timeout and respect the attempt context. Token refresh is shared and cancellation-aware. Ambiguous external failures are recovered by checking saved ownership.

```sh
make check build
make security
# TEST_DATABASE_URL must point to a new disposable database in Kubernetes.
make integration
# Supply TEST_KEYCLOAK_URL, TEST_KEYCLOAK_CLIENT_ID and TEST_KEYCLOAK_CLIENT_SECRET privately.
make keycloak-integration
```

Checks cover formatting, vet, lint, race tests, chart configuration, allocation, concurrent retries, rollback, instance assignments, ownership, configuration drift and worker recovery. Database tests refuse an already populated Platform database. Live Keycloak tests use owned disposable realms and skip occupied numeric names. Only matching test resources are removed.

The [security review and threat model](docs/reviews/platform-api-security.md) records the controls, live test evidence and the outstanding shared-Keycloak upgrade requirement.

## CI and layout

Pull requests and main pushes run Go checks, PostgreSQL integration, Helm checks and a container build. Version tags publish an image and OCI chart to ACR after checks pass, using repository-authorized Azure OIDC trust. Publishing does not deploy to AKS.

| Location | Responsibility |
| --- | --- |
| `src/cmd` | Startup, migrations and shutdown |
| `src/storage/partners` | Partner records, transactions, identity setup and worker |
| `src/storage/instances` | Registered instance addresses and availability |
| `src/storage/postgres`, `src/storage/schema` | Shared database pool and embedded migrations |
| `src/internal/keycloak` | Typed Admin API transport and resources |
| `src/internal/config` | Environment settings |
| `src/web_api` | Private HTTP handlers and JSON responses |
| `charts/browser-platform`, `scripts` | Deployment and verification |

The foundation was adapted from control-plane commit `bdfeacda929b8526408d0a1f9e23741ff715ca19`. Browser telemetry, device sessions and policies remain in the instance backend. [AGENTS.md](AGENTS.md) records repository rules. Published migrations are never rewritten.
