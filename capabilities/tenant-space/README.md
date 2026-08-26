# CAP-002: Tenant Space

This CAP-002 milestone provisions one quota-enabled tenant space through
Terraform orchestrated by Argo Workflows and destroys it through an
unconditional exit handler.

This phase also publishes a sanitized Terraform evidence artifact. Behavioral
assertions, Kubernetes evidence, JUnit results, and failed-run recovery
automation remain separate follow-up milestones.

## Workflow

The main workflow provisions the fixture and captures evidence before cleanup:

```text
prepare → terraform init → terraform plan → terraform apply
                                                    → collect Terraform evidence
                                                                  │
                                                                  └─ always:
                                                                     destroy
                                                                       ↓
                                                                     publish
```

Argo creates a 2 Gi `ReadWriteOnce` PVC for each workflow. All steps, including
the exit handler, mount that same claim. Terraform uses the local backend in a
run-specific directory:

```text
/workspace/runs/<workflow-uid>/state/terraform.tfstate
```

The Terraform working directory and saved plan use the same workflow UID. The
exit handler treats missing state as a safe no-op and otherwise runs
`terraform destroy`. Argo deletes the PVC only when the complete workflow,
including destroy, succeeds. Failed workflows retain their PVC so the state is
available for investigation and recovery. A workflow mutex serializes CAP-002
runs in the initial shared VLAN environment.

## Terraform evidence

The workflow publishes an Argo output artifact named `terraform-evidence` to
the Host namespace's default artifact repository. The development setup uses
MinIO for this repository. Depending on how far a run progresses, the artifact
contains:

```text
terraform/
├── metadata.json
├── plan-summary.json
├── applied-resources.json
├── cleanup.json
└── workflow-result.json
```

- `metadata.json` identifies the workflow, Target cluster, generated tenant,
  Terraform version, provider selections, and tenancy module version.
- `plan-summary.json` records the saved plan's SHA-256 digest, action counts,
  and resource types without storing planned values.
- `applied-resources.json` records allowlisted project, namespace, quota,
  network, and role-template information after a successful apply.
- `cleanup.json` records whether destroy ran, its exit code, and the count and
  types of any managed resources remaining in state.
- `workflow-result.json` records the workflow status seen by the exit handler.

The publisher runs after both successful and failed destroy attempts. Files
whose prerequisite stage was never reached are intentionally absent. Raw
Terraform state, the binary plan, generated variable files, credentials,
principal IDs, and unrestricted Terraform JSON are never published.

## Runner scripts and image

The Argo template defines orchestration, environment variables, mounts, and
resource limits. The shell implementation for each container step lives under
`scripts/`, with one script per step:

```text
scripts/
├── prepare.sh
├── terraform-init.sh
├── terraform-plan.sh
├── terraform-apply.sh
├── collect-terraform-evidence.sh
├── terraform-destroy.sh
└── publish-evidence.sh
```

The runner image carries Terraform 1.15.8, the CAP-002 fixture, and these
scripts. Rebuild it after changing either the Terraform files or a runner
script:

```bash
make terraform-runner-publish \
  RUNNER_IMAGE=REGISTRY/NAMESPACE/harvester-testsuite-terraform-runner
```

The image tag defaults to `0.1.0`. Resolve the pushed digest and replace
`newName` and `digest` in `workflow/kustomization.yaml`.

## Configure the Target

Create the credential Secret directly in the Host cluster:

```bash
kubectl -n argo create secret generic cap-002-target-credentials \
  --from-literal=rancher-api-token='REPLACE_WITH_RANCHER_TOKEN' \
  --from-file=harvester-kubeconfig=/absolute/path/to/target-harvester.yaml
```

Copy and edit the non-secret workflow parameter example:

```bash
cp capabilities/tenant-space/workflow/parameters/development.example.yaml \
  /tmp/cap-002-tenant-space-parameters.yaml
```

Replace the Rancher URL, cluster ID, role binding, and any development defaults
that differ in the Target environment. Values containing `REPLACE_` are rejected
by the `prepare` step before Terraform runs.

The workflow appends the first eight characters of its unique UID to
`project-name-prefix`. For example, the prefix `cap002-tenant-space-dev` can
produce:

```text
cap002-tenant-space-dev-a1b2c3d4
```

The generated name is printed by the `prepare` step. Each workflow submission
therefore creates an independently named tenant and stores its Terraform state
under that workflow's UID.

## Install and submit

```bash
kubectl apply -k capabilities/tenant-space/workflow

argo submit \
  --namespace argo \
  --from workflowtemplate/cap-002-tenant-space \
  --parameter-file /tmp/cap-002-tenant-space-parameters.yaml \
  --watch
```

The WorkflowTemplate contains development defaults for the project-name prefix,
VLAN, and resource quotas. A value can be overridden directly for a run, for
example:

```bash
argo submit \
  --namespace argo \
  --from workflowtemplate/cap-002-tenant-space \
  --parameter-file /tmp/cap-002-tenant-space-parameters.yaml \
  -p project-name-prefix=cap002-tenant-space-test \
  -p cpu-limit=12 \
  --watch
```

Workflow parameters are stored in the submitted Workflow and visible in Argo.
Only non-sensitive configuration belongs in the parameter file. The Rancher
token and Harvester kubeconfig remain in `cap-002-target-credentials`.

During the run, Terraform logs show the generated project, namespaces, quota,
role binding, and VM network being created and then removed. After a successful
workflow, inspect the `terraform-evidence` artifact in the Argo UI or MinIO and
verify that the tenant resources and workflow PVC no longer exist.

```bash
kubectl -n argo get pvc
```

If the workflow fails, do not delete its PVC until the tenant has been confirmed
absent or destroyed using the retained Terraform state. Automated recovery for
that case is a later milestone.
