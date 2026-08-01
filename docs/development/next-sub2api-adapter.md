# Historical Note: sub2api Read-only Adapter

This note captured an earlier adapter plan. The repository now manages sub2api,
new-api, and CPA through the per-instance outbound Connector. See
[current-state.md](current-state.md) for the active baseline.

## Current Management Path

```text
E2M Core
-> instance-bound Connector task
-> customer-side Connector
-> local gateway adapter
-> normalized gateway account or action result
```

The sub2api Connector adapter supports account normalization, schedulable state
changes, bad-key handling, and local test coverage. Gateway coordinates and
management credentials are configured only in the Connector's local UI and data
volume.

The earlier standalone observation path has been retired. Connector liveness and
gateway readiness are reported as non-sensitive runtime state during
authenticated task leasing; no host-runtime observation or separate liveness
endpoint is part of the deployment model.

## Deferred

- Additional packaging and enrollment-file ergonomics.
- Secret-bearing provisioning tasks with an end-to-end secret-safe transport.
