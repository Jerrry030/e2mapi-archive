# NocoBase Plugin Boundary

This directory reserves the future NocoBase integration.

NocoBase may host business configuration, work orders, approvals, and operator-facing forms. It should call E2M Core API and must not own:

- Agent protocol.
- Adapter state.
- Audit source of truth.
- Metering or billing source of truth.
- Customer credentials.

Expected future capabilities:

- work order forms
- approval screens
- notification route configuration
- runbook metadata editing
- customer-facing support views
