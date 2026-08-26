# Infrastructure

This directory contains reproducible Host-environment infrastructure and
configuration for running the test suite. Target capability fixtures do not
belong here; they live with their capability under `capabilities/`.

Infrastructure is separated by operational intent:

- [`development/`](development/README.md) provides the small-footprint reference
  used to prove the first vertical slice.
- [`production/`](production/README.md) records the production contract and will
  contain hardened deployment assets after the development setup is validated.

Committed infrastructure must be reusable outside the original lab. Hostnames,
credentials, tenant identifiers, VLAN IDs, storage classes, and similar local
values must be exposed as documented inputs or represented by safe examples.

Do not commit Terraform state, saved plans, populated variable files,
kubeconfigs, Secret resources, Helm values containing credentials, or exported
live-cluster metadata.

