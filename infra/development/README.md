# Development Environment

The development environment is the first reproducible Host reference. It is
intended for functional development and validation, not production workloads.

The reference will cover:

- Provisioning a Host tenant space and downstream Kubernetes cluster.
- Installing and configuring Argo Workflows.
- Configuring MinIO as an internal S3-compatible artifact repository.
- Providing Harvester-backed persistent storage for the CAP-002 Terraform
  workspace.
- Creating scoped workflow service accounts and RBAC.
- Referencing Target credentials through Kubernetes Secrets.
- Running smoke workflows that incrementally validate parameters, Terraform
  execution, artifact handling, and cleanup.

Development defaults may use single replicas, modest resource requests,
cluster-internal services, and Harvester-backed persistent storage. Every such
choice must be identified as development-only in the corresponding manifest or
values file.

CAP-002 stores local Terraform state on a per-workflow PVC in the Host cluster.
Argo deletes the claim after successful exit-handler cleanup and retains it when
the workflow fails. MinIO remains reserved for future sanitized Argo results
and evidence; Terraform state must never be uploaded there as an artifact.
