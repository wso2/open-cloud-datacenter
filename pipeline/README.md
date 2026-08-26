# Pipeline

This directory owns workflow behavior shared across capabilities and the future
aggregate suite workflow. Capability-specific workflows live with their modules
under `capabilities/<descriptive-name>/workflow/`.

The intended layout is:

```text
pipeline/
├── shared/       # Reusable workflow templates and lifecycle components
├── aggregate/    # Future workflow that executes a capability selection
├── rbac/         # Pipeline-wide service accounts and least-privilege roles
└── janitor/      # Scheduled recovery of expired fixtures
```

The first shared executable asset is the Terraform runner image under
`images/terraform-runner/`. The remaining subdirectories will be introduced
with executable manifests rather than empty placeholders.

## Capability workflows

Every capability owns an independently runnable Argo `WorkflowTemplate`. A team
must be able to submit its capability without waiting for other modules or for
the aggregate workflow to exist.

A capability workflow owns:

- Capability-specific parameters and validation.
- Fixture provisioning and destruction calls.
- Behavioral test selection.
- Capability-specific evidence declarations.
- Its exit path and use of the shared cleanup contract.

## Shared components

Shared pipeline assets will provide reusable:

- Preflight and common input validation.
- Run identity and environment-lock operations.
- Terraform runner conventions.
- Result and artifact publication.
- Cleanup status reporting.
- Approved service accounts and least-privilege RBAC.
- Scheduled recovery of expired fixtures.

Shared components must expose versioned interfaces so one capability change does
not unexpectedly break other workflows.

## Aggregate workflow

The aggregate workflow is intentionally deferred until the initial capability
set approaches a release. It will:

- Discover capability metadata, optionally producing a generated catalog,
  rather than maintain a hard-coded task list.
- Select capabilities by ID, label, priority, or release profile.
- Invoke each capability's published workflow entrypoint.
- Apply suite-level ordering and concurrency limits.
- Combine capability outcomes into a suite-level result without hiding the
  individual result bundles.

The aggregate workflow orchestrates capability workflows; it does not absorb or
duplicate their implementation.

## Security contract

Capability workflows receive references to Host-managed Target credential
Secrets. Credentials and kubeconfig contents must never be accepted as ordinary
workflow parameters. Shared components must preserve least privilege and redact
sensitive data before artifact publication.
