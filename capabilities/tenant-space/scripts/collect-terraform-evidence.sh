#!/bin/sh
set -eu

runDir="/workspace/runs/$WORKFLOW_UID"
terraformDir="$runDir/terraform"
evidenceDir="$runDir/evidence/terraform"
collectedAt="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cd "$terraformDir"

projectId="$(terraform output -raw project_id)"
projectName="$(terraform output -raw project_name)"
namespaceIds="$(terraform output -json namespace_ids)"
networkNamespace="$(terraform output -raw network_namespace)"
networkNamespaceId="$(terraform output -raw network_namespace_id)"

terraform show -json | jq \
  --arg collected_at "$collectedAt" \
  --arg project_id "$projectId" \
  --arg project_name "$projectName" \
  --argjson namespace_ids "$namespaceIds" \
  --arg network_namespace "$networkNamespace" \
  --arg network_namespace_id "$networkNamespaceId" '
    def modules:
      ., (.child_modules[]? | modules);

    [.values.root_module | modules | .resources[]?] as $resources
    | first(
        $resources[]
        | select(.address == "module.tenant_space.rancher2_project.this")
      ) as $project
    | if $project == null then
        error("tenant project is missing from Terraform state")
      else
        {
          schemaVersion: "testsuite.opencloud.wso2.com/v1alpha1",
          kind: "TerraformAppliedResources",
          collectedAt: $collected_at,
          project: {
            id: $project_id,
            name: $project_name,
            clusterId: $project.values.cluster_id,
            projectLimit: ($project.values.resource_quota[0].project_limit[0] // null),
            namespaceDefaultLimit: ($project.values.resource_quota[0].namespace_default_limit[0] // null)
          },
          namespaces: {
            workload: $namespace_ids,
            network: {
              name: $network_namespace,
              id: $network_namespace_id
            },
            resources: [
              $resources[]
              | select(.type == "rancher2_namespace")
              | {
                  terraformName: .name,
                  name: .values.name,
                  id: .values.id,
                  quota: (.values.resource_quota[0].limit[0] // null)
                }
            ]
          },
          networks: [
            $resources[]
            | select(.type == "harvester_network")
            | {
                terraformName: .name,
                id: .values.id,
                name: .values.name,
                namespace: .values.namespace,
                vlanId: .values.vlan_id,
                clusterNetworkName: .values.cluster_network_name,
                routeMode: .values.route_mode
              }
          ],
          roleBindings: {
            tenant: [
              $resources[]
              | select(
                  .type == "rancher2_project_role_template_binding"
                  and .name == "this"
                )
              | {roleTemplateId: .values.role_template_id}
            ],
            sharedImageAccess: [
              $resources[]
              | select(
                  .type == "rancher2_project_role_template_binding"
                  and .name == "shared_image_access"
                )
              | {roleTemplateId: .values.role_template_id}
            ]
          }
        }
      end
  ' >"$evidenceDir/applied-resources.json"

echo "sanitized Terraform apply evidence collected"
