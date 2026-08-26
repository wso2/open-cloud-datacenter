#!/bin/sh
set -eu

runDir="/workspace/runs/$WORKFLOW_UID"
stateFile="$runDir/state/terraform.tfstate"
evidenceDir="$runDir/evidence/terraform"
cleanupFile="$evidenceDir/cleanup.json"
mkdir -p "$evidenceDir"

managed_resources() {
  if [ ! -s "$stateFile" ]; then
    printf '[]'
    return
  fi

  jq -c '[
    .resources[]?
    | select(.mode == "managed")
    | {
        module: (.module // "root"),
        type,
        name,
        instances: (.instances | length)
      }
  ]' "$stateFile" 2>/dev/null || printf '[]'
}

write_cleanup_evidence() {
  attempted="$1"
  succeeded="$2"
  exitCode="$3"
  message="$4"
  remainingResources="$(managed_resources)"
  remainingCount="$(printf '%s' "$remainingResources" | jq '[.[].instances] | add // 0')"

  jq -n \
    --arg collected_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson attempted "$attempted" \
    --argjson succeeded "$succeeded" \
    --argjson terraform_exit_code "$exitCode" \
    --arg message "$message" \
    --argjson managed_resources_before "$managedResourcesBefore" \
    --argjson remaining_count "$remainingCount" \
    --argjson remaining_resources "$remainingResources" \
    '{
      schemaVersion: "testsuite.opencloud.wso2.com/v1alpha1",
      kind: "TerraformCleanupResult",
      collectedAt: $collected_at,
      attempted: $attempted,
      succeeded: $succeeded,
      terraformExitCode: $terraform_exit_code,
      message: $message,
      managedResourcesBefore: $managed_resources_before,
      managedResourcesRemaining: $remaining_count,
      remainingResources: $remaining_resources
    }' >"$cleanupFile"
}

if [ ! -s "$stateFile" ]; then
  managedResourcesBefore=0
  write_cleanup_evidence false true 0 "no Terraform state found; cleanup was not required"
  echo "no Terraform state found; cleanup is not required"
  exit 0
fi

resourcesBefore="$(managed_resources)"
managedResourcesBefore="$(printf '%s' "$resourcesBefore" | jq '[.[].instances] | add // 0')"

terraformDir="$runDir/terraform"
[ -d "$terraformDir" ] || {
  write_cleanup_evidence true false 1 "Terraform working directory was missing"
  echo "Terraform state exists but the working directory is missing" >&2
  exit 1
}

set +e
cd "$terraformDir"
terraform init -input=false -no-color -lockfile=readonly
initExitCode=$?
if [ "$initExitCode" -ne 0 ]; then
  write_cleanup_evidence true false "$initExitCode" "terraform init failed before destroy"
  exit "$initExitCode"
fi

terraform destroy \
  -auto-approve \
  -input=false \
  -no-color
destroyExitCode=$?

if [ "$destroyExitCode" -ne 0 ]; then
  write_cleanup_evidence true false "$destroyExitCode" "terraform destroy failed"
  exit "$destroyExitCode"
fi

remainingResources="$(managed_resources)"
remainingCount="$(printf '%s' "$remainingResources" | jq '[.[].instances] | add // 0')"
if [ "$remainingCount" -ne 0 ]; then
  write_cleanup_evidence true false 1 "terraform destroy completed but managed resources remain in state"
  echo "Terraform managed resources remain in state after destroy" >&2
  exit 1
fi

write_cleanup_evidence true true 0 "tenant resources destroyed successfully"
echo "tenant resources destroyed successfully"
