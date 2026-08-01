# Backstage Plugin Boundary

This directory reserves the future Backstage plugin integration.

The plugin should call E2M Core API and must not own:

- Agent protocol.
- Adapter state.
- Workflow state.
- Audit logs.
- Metering or billing events.
- Customer credentials.

Expected future views:

- instance catalog
- instance health
- recent errors
- Agent status
- runbook/workflow links
- deployment links
