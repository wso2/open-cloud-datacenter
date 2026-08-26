# Capabilities

This directory contains independent acceptance-test modules for Harvester and
Rancher capabilities.

Each module owns its workflow, fixture definition, behavioral tests, evidence
declaration, and capability metadata:

```text
descriptive-name/
├── capability.yaml
├── workflow/
│   └── workflow-template.yaml
├── fixtures/
├── tests/
└── evidence.yaml
```

Capability IDs are stable public identifiers. Directory names use readable
capability names such as `tenant-space`; `capability.yaml` records the stable ID
such as `CAP-002`.

## Module responsibilities

A capability module must:

- Declare its required inputs, labels, timeout, lock scope, and outputs.
- Provide a capability-owned `WorkflowTemplate` that can be submitted and tested
  without the aggregate suite workflow.
- Create resources with a unique run ID.
- Use Terraform only for fixture lifecycle.
- Verify observable behavior through the shared Go test runner.
- Collect redacted diagnostics defined by `evidence.yaml`.
- Support repeatable cleanup after partial provisioning or assertion failures.
- Publish results through the common JUnit, JSON, evidence, and log contract.

Capability-specific orchestration and behavior must remain inside the module.
Reusable workflow steps, locking contracts, evidence publication, and cleanup
policy belong to `pipeline/` or `internal/`.

## Execution model

During capability development, a team submits only its capability
`WorkflowTemplate`. This keeps feedback focused and avoids requiring incomplete
capabilities to run together.

At the initial release stage, an aggregate workflow under `pipeline/aggregate/`
will discover `capability.yaml` metadata and invoke the selected workflow
entrypoints. The catalog, if one is needed by the runner, will be generated from
those files instead of being edited by every capability team. A capability
therefore remains independently runnable after it is included in full-suite
execution.

## Initial capability

The first implementation is [`tenant-space`](tenant-space/), with stable ID
`CAP-002`. Its initial phase covers Terraform provisioning through Argo using a
per-workflow state PVC and an unconditional destroy exit handler. Output
collection, failed-run recovery, and behavioral assertions will be added
incrementally without changing the capability's public identity.
