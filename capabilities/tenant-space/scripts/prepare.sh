#!/bin/sh
set -eu

fail() {
  echo "configuration error: $1" >&2
  exit 1
}

contains_placeholder() {
  case "$1" in
    ""|*REPLACE_*) return 0 ;;
    *) return 1 ;;
  esac
}

for value in "$rancherUrl" "$harvesterClusterId" \
  "$projectNamePrefix" "$vmNetworkVlanId" "$cpuLimit" \
  "$memoryLimit" "$storageLimit" "$groupRoleBindingsJson" \
  "$workflowUid"; do
  contains_placeholder "$value" && fail "a workflow parameter is empty or still contains REPLACE_"
done

printf '%s' "$rancherUrl" | grep -Eq '^https://[^[:space:]]+$' || fail "rancherUrl must use HTTPS"
printf '%s' "$projectNamePrefix" | grep -Eq '^cap002-[a-z0-9]([a-z0-9-]*[a-z0-9])?$' || fail "projectNamePrefix must start with cap002- and use DNS-label characters"
printf '%s' "$vmNetworkVlanId" | grep -Eq '^[0-9]+$' || fail "vmNetworkVlanId must be numeric"
[ "$vmNetworkVlanId" -ge 1 ] && [ "$vmNetworkVlanId" -le 4094 ] || fail "vmNetworkVlanId must be between 1 and 4094"
[ -n "$TF_VAR_rancher_api_token" ] || fail "Rancher API token is empty"
[ -s /credentials/harvester/kubeconfig ] || fail "Harvester kubeconfig is missing or empty"

runSuffix="$(printf '%s' "$workflowUid" | tr -d '-' | cut -c 1-8)"
[ "${#runSuffix}" -eq 8 ] || fail "workflow UID could not produce an 8-character run suffix"
projectName="${projectNamePrefix}-${runSuffix}"
[ "${#projectName}" -le 63 ] || fail "generated project name must not exceed 63 characters"

printf '%s' "$groupRoleBindingsJson" | jq -e '
  type == "array" and
  all(.[];
    (.role_template_id | type == "string" and length > 0) and
    ([.group_principal_id, .group_id, .user_principal_id, .user_id]
      | map(select(. != null and . != "")) | length) == 1
  )
' >/dev/null || fail "groupRoleBindingsJson is invalid"

runDir="/workspace/runs/$workflowUid"
terraformDir="$runDir/terraform"
evidenceDir="$runDir/evidence/terraform"
mkdir -p "$terraformDir" "$runDir/state" "$evidenceDir" \
  /workspace/home /workspace/plugin-cache
rm -f "$terraformDir"/*.tf \
  "$terraformDir/.terraform.lock.hcl" \
  "$terraformDir/terraform.auto.tfvars.json"
cp -R /opt/testsuite/capabilities/tenant-space/terraform/. "$terraformDir/"

jq -n \
  --arg rancher_url "$rancherUrl" \
  --arg harvester_cluster_id "$harvesterClusterId" \
  --arg harvester_kubeconfig_path /credentials/harvester/kubeconfig \
  --arg project_name "$projectName" \
  --arg cpu_limit "$cpuLimit" \
  --arg memory_limit "$memoryLimit" \
  --arg storage_limit "$storageLimit" \
  --argjson vm_network_vlan_id "$vmNetworkVlanId" \
  --argjson group_role_bindings "$groupRoleBindingsJson" \
  '{
    rancher_url: $rancher_url,
    harvester_cluster_id: $harvester_cluster_id,
    harvester_kubeconfig_path: $harvester_kubeconfig_path,
    project_name: $project_name,
    cpu_limit: $cpu_limit,
    memory_limit: $memory_limit,
    storage_limit: $storage_limit,
    vm_network_vlan_id: $vm_network_vlan_id,
    group_role_bindings: $group_role_bindings
  }' >"$terraformDir/terraform.auto.tfvars.json"

terraform -chdir="$terraformDir" version -json | jq \
  --arg workflow_name "$workflowName" \
  --arg workflow_uid "$workflowUid" \
  --arg project_name "$projectName" \
  --arg target_cluster_id "$harvesterClusterId" \
  --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{
    schemaVersion: "testsuite.opencloud.wso2.com/v1alpha1",
    kind: "TerraformRunMetadata",
    generatedAt: $generated_at,
    capabilityId: "CAP-002",
    capabilityName: "tenant-space",
    workflow: {
      name: $workflow_name,
      uid: $workflow_uid
    },
    target: {
      clusterId: $target_cluster_id
    },
    tenant: {
      projectName: $project_name
    },
    terraform: {
      version: .terraform_version,
      platform: .platform,
      providerSelections: .provider_selections,
      module: {
        source: "github.com/wso2/open-cloud-datacenter//modules/tenancy/tenant-space",
        version: "terraform/v0.2.0"
      }
    }
  }' >"$evidenceDir/metadata.json"

echo "tenant project name: $projectName"
