# Mutex Browser Platform

Private Go service for the shared Mutex Platform API, backed by PostgreSQL.

This repository contains the service foundation. Partner/client/site endpoints, console realm discovery, Keycloak setup and staff permissions are not implemented yet. Their intended behavior is recorded in [ADR-000: Identity and access](docs/adrs/adr-000-identity-and-access.md) and [ADR-001: Internal Platform API](docs/adrs/adr-001-platform-api.md).

## What runs today

| Component | Behavior |
| --- | --- |
| HTTP server | Loopback by default; bounded timeouts and graceful shutdown. |
| PostgreSQL | Required at startup; bounded connection pool and embedded Goose migrations. |
| Migrations | Create the `platform` schema. An advisory lock coordinates simultaneous starts. No customer tables exist yet. |
| Liveness | `GET /internal/v1/health` checks that the server is running. |
| Readiness | `GET /internal/v1/health/ready` checks PostgreSQL with a two-second timeout. |
| Deployment | ClusterIP service, no ingress, non-root container and a policy denying incoming pod traffic. |

The initial service has no application authentication. Operator access uses local loopback or Kubernetes port-forwarding authorized by cluster permissions. The ADRs describe the eventual console integration; the service does not yet accept console management requests. Caller identity, staff access enforcement and private service TLS must be implemented before that integration.

## Local development

Use the shared `mutexai-dev` Kubernetes cluster, as in the browser control-plane repository. The scripts build the application image, load it into Kind and install the Helm release. PostgreSQL runs in Kubernetes; there is no standalone database container or Compose workflow.

Prerequisites:

- `kind`, `kubectl`, `helm` and the container engine used by Kind.
- A reachable `kind-mutexai-dev` context.
- A dedicated Platform database and user in the local Kubernetes PostgreSQL deployment.
- Private Helm values containing its connection settings and password, or an `existingSecret` providing the password.

Prepare local values once:

```sh
mkdir -p .local
cp charts/browser-platform/values-local.example.yaml .local/values.yaml
chmod 600 .local/values.yaml
```

Fill in `postgres.password` for the dedicated database user. `.local/` is excluded from Git and the image build context. The chart creates `browser-platform-secrets`; there is no separate `kubectl create secret` step for the application.

Install or rebuild after code changes:

```sh
make install
kubectl --context kind-mutexai-dev -n mutexai-platform \
  port-forward deployment/browser-platform 8002:8080
```

In another terminal:

```sh
curl http://127.0.0.1:8002/internal/v1/health/ready
```

`make deploy` is an alias for `make install`. The installer validates Helm values before building. Override `PLATFORM_VALUES_FILE`, `PLATFORM_CLUSTER_NAME` or `PLATFORM_NAMESPACE` when needed; the context is always `kind-<cluster name>`. It does not switch the current context, restart the shared cluster or create databases.

`make uninstall` removes the application and its chart-created Secret. It retains the database, namespace, private local values and externally managed Secrets. Reinstall with the same private values to preserve the database credentials.

## Configuration and secrets

The application reads environment variables only. Helm values configure those variables; the Go process does not read YAML or fetch Kubernetes Secrets itself. This follows the browser control-plane chart's convention:

| Configuration | Helm source | Process input |
| --- | --- | --- |
| PostgreSQL address, database, username and TLS mode | `postgres.host`, `port`, `database`, `username`, `sslMode` | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DATABASE`, `POSTGRES_USERNAME`, `POSTGRES_SSLMODE` |
| Local database password | `postgres.password` in private values; chart-created Secret | `POSTGRES_PASSWORD` through `envFrom.secretRef` |
| Production database password | `existingSecret`, supplied by the environment's secret-management setup | The same `POSTGRES_PASSWORD` variable |
| Log level | `logLevel` | `LOG_LEVEL` |
| Server timeouts | `server.*TimeoutSec` | `HTTP_READ_TIMEOUT_SEC`, `HTTP_READ_HEADER_TIMEOUT_SEC`, `HTTP_WRITE_TIMEOUT_SEC`, `HTTP_IDLE_TIMEOUT_SEC`, `HTTP_SHUTDOWN_TIMEOUT_SEC` |

Production sets `existingSecret` and leaves `postgres.password` empty. The chart then creates no password Secret. Both modes use the same deployment, application and environment-variable names. Passwords are absent from the Deployment's inline environment values. Chart-managed password changes update the pod-template checksum; externally managed Secret changes require a rollout restart after rotation.

PostgreSQL settings are required except the port (`5432`) and TLS mode (`verify-full`). Local values explicitly select `disable` for the development PostgreSQL server, which has no TLS. The chart binds HTTP to `0.0.0.0:8080`; host debugging defaults to `127.0.0.1:8080` unless `LISTEN_ADDR` is set. Server timeouts default to 30 seconds for reading, writing and shutdown, five seconds for headers, and 120 seconds for idle connections. The pod termination grace period exceeds its configured shutdown timeout.

Startup connects to PostgreSQL, runs migrations under an advisory lock and starts HTTP. A database outage after startup makes readiness return `503`, while liveness stays healthy. Readiness recovers when PostgreSQL returns. `.env.example` is only for optional host debugging through a PostgreSQL port-forward; the process does not load it automatically.

Each process shares one PostgreSQL pool: at most 10 open connections, up to five idle connections, a 30-minute connection lifetime and a five-minute idle limit. Startup connection checks have a five-second deadline. HTTP requests drain before the pool closes. Every database operation must carry a context with a deadline; the connection timeout alone does not limit query duration. Allow for all replicas, including rollout overlap, when sizing PostgreSQL connections. Pool recovery does not make failed writes safe to retry automatically.

## Build and verification

```sh
make build
make test
make check
make image
```

`make check` requires `golangci-lint` v2.12.2, Helm and Python 3. Chart checks exercise both local and externally managed Secret modes without using real credentials. Make commands disable the parent Go workspace so this repository builds independently. For direct Go commands in a parent workspace, set `GOWORK=off`.

Database integration tests require a new, explicitly selected **disposable database** for each run. They refuse to run if the `platform` schema or Goose migration history already exists. The test applies migrations concurrently and retries them without deleting existing data. Never use an application database for these tests.

```sh
TEST_DATABASE_URL='postgres://platform:platform@127.0.0.1:5432/platform_test?sslmode=disable' make integration
```

For local integration tests, create a disposable `platform_test` database in Kubernetes and port-forward its PostgreSQL service to `127.0.0.1:5432` before running the command. The tests also cover pool exhaustion, query cancellation, connection replacement and closure. CI supplies a separate temporary PostgreSQL service and runs these tests alongside the unit tests.

Add future SQL migrations under `src/storage/schema`; the binary embeds them. `make migrate` applies migrations without starting HTTP. Published migration files should not be rewritten.

## Deployment boundary

The chart grants no Kubernetes permissions. Operators need existing permission to port-forward. Its NetworkPolicy requires a network plugin that enforces Kubernetes policies; no public ingress or load balancer is created. The HTTP listener does not terminate TLS. The initial operator connection uses Kubernetes port-forwarding; future service callers need approved network access and service transport configuration.

PostgreSQL and its database roles are managed separately from the application chart. The chart delivers their credentials through its local or externally managed Secret mode. For deployments using published images, set `image.registry` and `image.tag` in Helm values. The local installer instead loads a unique development image into Kind and uses `image.pullPolicy=Never`.

## CI and releases

Pull requests and main-branch pushes run build, formatting, vet, lint, race tests, PostgreSQL integration tests, Helm checks and a container build. Pull requests and main-branch builds do not log in to Azure or publish artifacts. The container job waits for the Go and Helm checks and builds once per run.

The registry name is derived from `ACR_LOGIN_SERVER`, using the same Azure registry and organization variables as control-plane. Tags in `vMAJOR.MINOR.PATCH` format publish the image `mutexai-browser-platform` and the chart `browser-platform` after all checks pass. The release workflow requires repository or organization variables `ACR_LOGIN_SERVER`, `ACR_PUSH_CLIENT_ID`, `AZURE_TENANT_ID` and `AZURE_SUBSCRIPTION_ID`, plus Azure OIDC trust for this repository. No registry or cloud account is hard-coded.

## Source and layout

The foundation was derived from `mutex-ai/mutexai-browser-controlplane` commit `bdfeacda929b8526408d0a1f9e23741ff715ca19`. Server lifecycle, logging conventions, HTTP helpers and tooling were retained or adapted. The local install/uninstall scripts follow the CP's Kind-and-Helm workflow. Browser functionality, old authentication assumptions and ClickHouse/Redis storage were excluded. The source repository and its uncommitted changes were preserved.

```text
src/cmd/              Startup and shutdown
src/internal/config/ Environment settings
src/server/           HTTP lifecycle
src/storage/postgres/ Connection pool
src/storage/schema/   Embedded PostgreSQL migrations
src/web_api/          Private routes and HTTP helpers
charts/browser-platform/
scripts/              Local Kubernetes install and uninstall
docs/adrs/
```

The ADRs were copied from the infrastructure repository as the design baseline. Implementation status is described above; a proposed endpoint in an ADR is not an implemented route.
