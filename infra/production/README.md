# Production Environment

Production infrastructure will be added after the development pipeline and
CAP-002 vertical slice are validated.

The production reference must address:

- Availability and upgrade strategy for Argo Workflows.
- Durable artifact storage, retention, backup, and recovery.
- External secret management and credential rotation.
- Network policies and restricted Host-to-Target egress.
- Resource sizing, quotas, and workflow concurrency.
- Authentication, authorization, and approved workflow submission.
- Monitoring, alerting, audit logs, and operational runbooks.
- Recovery of interrupted runs and expired Target fixtures.

Development manifests must not be promoted to production without an explicit
review of these controls.

