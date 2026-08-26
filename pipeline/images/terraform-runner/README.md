# Terraform Runner Image

This non-root image supplies Terraform, Git, jq, CA certificates, and a POSIX
shell. For the initial CAP-002 milestone, it also carries the tenant-space
Terraform fixture and capability-owned runner scripts. The Argo template calls
those scripts by their paths under `/opt/testsuite/capabilities/tenant-space/`.

Build and push the image to a registry reachable from the Host cluster:

```bash
make terraform-runner-publish \
  RUNNER_IMAGE=REGISTRY/NAMESPACE/harvester-testsuite-terraform-runner
```

`RUNNER_IMAGE_TAG` defaults to `0.1.0`, and `RUNNER_PLATFORM` defaults to
`linux/amd64`. Override them when required:

```bash
make terraform-runner-publish \
  RUNNER_IMAGE=REGISTRY/NAMESPACE/harvester-testsuite-terraform-runner \
  RUNNER_IMAGE_TAG=0.1.1
```

Use `terraform-runner-build` and `terraform-runner-push` when the operations
must be run separately. The Makefile deliberately requires `RUNNER_IMAGE`; it
does not contain a default registry destination.

Resolve the pushed digest, then replace `newName` and `digest` in
`capabilities/tenant-space/workflow/kustomization.yaml`. The deployed workflow
must use a digest, not a mutable tag.

Rebuild and republish the image whenever either
`capabilities/tenant-space/fixtures/terraform/` or
`capabilities/tenant-space/scripts/` changes.

Verify the local image before publishing it:

```bash
make terraform-runner-verify \
  RUNNER_IMAGE=REGISTRY/NAMESPACE/harvester-testsuite-terraform-runner
```
