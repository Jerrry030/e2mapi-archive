# E2M Connector Development

`app/e2m-agent` builds the customer-side E2M Connector. The executable name and
image repository remain compatible, but its only runtime role is the outbound
Connector. It does not run a separate observation loop, access the host
container runtime, or declare which user owns it.

Connector manages only the station owner's own gateway pool. Platform upstream
accounts, downstream API keys, metering, and request forwarding are native E2M
Core responsibilities and must never be configured through this Connector.

## Runtime Model

- Deploy one Connector for exactly one gateway instance.
- Create its enrollment from that instance in Core. The one-time token is bound
  to both the user and instance before installation.
- Mount the enrollment token as a private file. On first boot, the Connector
  exchanges it for a per-Connector token and saves that token in its own data
  volume.
- Configure the gateway address, type, authentication method, and management
  credential through the loopback-only local UI.
- Poll Core over outbound HTTPS, lease tasks for this Connector, call the local
  gateway adapter, and return normalized non-secret results.
- Do not share the data volume, enrollment file, Connector ID, or instance ID
  between instances.

Core never receives gateway coordinates or management credentials. Connector
runtime state contains only non-sensitive readiness and task status.

## Required Configuration

```text
E2M_CORE_URL
E2M_CONNECTOR_ID (required together with E2M_INSTANCE_ID on first start)
E2M_INSTANCE_ID (required together with E2M_CONNECTOR_ID on first start)
E2M_CONNECTOR_ENROLL_TOKEN_FILE
E2M_CONNECTOR_TOKEN_FILE
E2M_CONNECTOR_IDENTITY_FILE (optional; defaults inside E2M_AGENT_DATA_DIR)
E2M_LOCAL_UI_ADDR
```

`E2M_CONNECTOR_ENROLL_TOKEN_FILE` is read only for first enrollment.
`E2M_CONNECTOR_TOKEN_FILE`, `connector-identity.json`, `gateway-config.json`,
and `local-ui.token` live in `/var/lib/e2m-agent` on a dedicated named volume.
The first start atomically saves the configured Connector ID and instance ID.
Later starts can omit both environment values and restore them from the volume;
partial or conflicting values fail closed.

Use the console-generated install guide rather than constructing these values by
hand. There is no shared Connector token and no user/owner environment variable.

## Compose Example

```yaml
services:
  connector:
    image: ghcr.io/jerrry030/e2m-agent:latest
    restart: unless-stopped
    environment:
      E2M_CORE_URL: https://core.example.com
      E2M_CONNECTOR_ID: conn-example
      E2M_INSTANCE_ID: inst-example
      E2M_CONNECTOR_ENABLED: "true"
      E2M_CONNECTOR_ENROLL_TOKEN_FILE: /run/secrets/e2m_connector_enrollment
      E2M_CONNECTOR_TOKEN_FILE: /var/lib/e2m-agent/connector.token
      E2M_CONNECTOR_IDENTITY_FILE: /var/lib/e2m-agent/connector-identity.json
      E2M_LOCAL_UI_ADDR: 0.0.0.0:18081
    ports:
      - 127.0.0.1:18081:18081
    volumes:
      - connector-data:/var/lib/e2m-agent
    secrets:
      - e2m_connector_enrollment

volumes:
  connector-data:

secrets:
  e2m_connector_enrollment:
    file: ./e2m-connector-enrollment-token
```

The host port is loopback-only. Use a different loopback port and named volume
for every additional Connector.

## Local UI

After startup, read `local-ui.token` from the dedicated Connector data volume
and open:

```text
http://127.0.0.1:18081/#token=e2m_local_...
```

Select the gateway kind and authentication mode, enter the gateway's locally
reachable URL and management credential, test, and save. The Connector writes
`gateway-config.json` with private file permissions. The local API never returns
credential values.

For a remote Connector host, leave the UI bound to loopback and tunnel it:

```sh
ssh -L 18081:127.0.0.1:18081 user@connector-host
```

## Adapter Boundary

The Connector has local adapters for sub2api, new-api, and CPA. Current task
types cover gateway health, account listing, schedulable state changes, and
account switching, plus the scheduling barrier used to fence those mutations.
Secret-bearing account provisioning is retired from the current product because
platform delivery is owned by E2M Core; sensitive-looking task JSON is rejected.

The local Binding API, upstream-intelligence APIs, quality-probe settings, and
CPA usage-queue switch are not available. Existing on-disk values can be read
for upgrade compatibility but are not exposed or used.

Further hardening still includes mTLS and signed task payloads in addition to
per-Connector tokens.
