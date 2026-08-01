# Deployment Templates

The active deployment model has one product boundary:

- E2M Core owns the console, identity, platform upstreams, groups, downstream
  keys, wallet, metering, scheduling, retry, and `/v1/*` forwarding;
- one customer-side Connector is installed for each customer-owned gateway and
  performs management-only operations against that gateway.

Do not deploy Sub2API as an independent E2M data plane or management plane. Do
not expose a Sub2API login, admin port, API key, wallet, database, or Redis as
part of an E2M installation.

## Connector Rules

- Run exactly one Connector for each customer-owned gateway instance.
- Create enrollment from that instance in E2M. The one-time token is already
  bound to its user and instance.
- Mount the enrollment token from a private file, never an environment value.
- Give every Connector a distinct `/var/lib/e2m-agent` data volume. Never share
  identities, tokens, gateway configuration, or local UI tokens.
- Configure the customer's gateway management URL and credential only through
  the Connector local UI. E2M Core must not receive them.
- Publish the local UI on loopback only; use an SSH tunnel for a remote host.
- Do not mount the Docker socket.

Connector performs only health checks, account listing, schedulable changes,
account switching, and the scheduling barrier. It does not receive platform
upstream credentials, issue platform keys, adjust balances, meter platform
traffic, or proxy downstream requests.

## Active Templates

- `compose/e2m-core-real-gateways.compose.yml`: minimal local E2M platform
  management and forwarding acceptance stack. Despite its historical filename,
  it contains no standalone gateway product.
- `compose/e2m-skeleton.compose.yml`: compact Core/Connector example; validate
  it against the current Connector rules before production use.
- `compose/e2m-core-prod.compose.yml`: historical production-oriented template;
  audit it against the current single-product boundary before use.
- `compose/e2m-core-dev.compose.yml`: broader historical development fixture;
  not the current product acceptance stack.
- `dokploy/e2m-skeleton.compose.yml`: prebuilt-image example.

## Minimal Local Acceptance

From the repository root:

```powershell
.\scripts\bootstrap-real-gateways.ps1
```

This command starts exactly four services, using `--remove-orphans` to stop
previous standalone gateway, NewAPI, CPA, Sub2API, Redis, or Connector
containers without deleting their volumes:

```text
postgres
mock-openai
mock-openai-fail
e2m-core
```

The mock upstream has no host-published port. The script calls only E2M APIs:

1. log into E2M;
2. create an E2M platform group;
3. create two E2M platform upstreams in the same group: a deterministic 503
   fixture followed by a successful fixture;
4. add local test balance to the E2M user;
5. issue an E2M downstream API key;
6. send JSON and streaming SSE requests to E2M
   `/v1/chat/completions`;
7. prove retryable `503 -> second upstream` transfer and read the released and
   settled usage records back from E2M.

The generated test key is written under
`deployments/runtime/platform-forwarding` and must not be used outside local
development. The script never logs into or calls Sub2API.

To repeat bootstrap and verification against an already running stack:

```powershell
.\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp
```

The script never performs `down -v` and does not delete database or Connector
data volumes. Idempotency keys make repeated local management mutations safe;
the e2m-core platform endpoints implement those contracts. When running the
mock upstream outside Compose for a host-level diagnostic, override its URL
without changing the fact that all management and downstream requests still
enter E2M:

```powershell
.\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp `
  -PlatformUpstreamBaseUrl http://127.0.0.1:18093/v1
```

## Customer Connector Installation

1. Create the customer-owned gateway instance in E2M.
2. Generate its Connector installation guide.
3. Write the one-time token into the guide's private enrollment file.
4. Start the Connector with its own identity and data volume.
5. Read `local-ui.token` from that volume.
6. Open `http://127.0.0.1:<port>/#token=<token>` and configure the local gateway.

For a remote Connector host:

```sh
ssh -L 18081:127.0.0.1:18081 user@connector-host
```

For the current factual boundary, see
[`docs/development/platform-boundaries.md`](../../docs/development/platform-boundaries.md).
