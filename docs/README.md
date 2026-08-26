# Documentation

This directory contains project documentation that is more detailed than the
root [README](../README.md).

## Documentation map

- [Architecture](architecture.md): system boundaries, components, invariants,
  and the pipeline lifecycle.
- [Development environment setup](../infra/development/README.md).
- [CAP-002 operations](../capabilities/tenant-space/README.md), including the
  provisioning, exit-handler cleanup, and per-workflow state PVC.
- Production environment setup: added after the development vertical slice is
  validated.
- Capability authoring guide: added with the executable capability contract.
- Operations and troubleshooting: added with the first runnable pipeline.

Documentation must distinguish implemented behavior from planned behavior and
must not include credentials or environment-specific secrets.
