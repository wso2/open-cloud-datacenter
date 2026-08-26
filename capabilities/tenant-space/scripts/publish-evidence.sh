#!/bin/sh
set -eu

evidenceDir="/workspace/runs/$WORKFLOW_UID/evidence/terraform"
mkdir -p "$evidenceDir"

if [ ! -s "$evidenceDir/metadata.json" ]; then
  jq -n \
    --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg workflow_name "$WORKFLOW_NAME" \
    --arg workflow_uid "$WORKFLOW_UID" \
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
      preparationCompleted: false
    }' >"$evidenceDir/metadata.json"
fi

jq -n \
  --arg recorded_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg workflow_status "$WORKFLOW_STATUS" \
  '{
    schemaVersion: "testsuite.opencloud.wso2.com/v1alpha1",
    kind: "WorkflowResult",
    recordedAt: $recorded_at,
    statusBeforeExitHandler: $workflow_status
  }' >"$evidenceDir/workflow-result.json"

for evidenceFile in "$evidenceDir"/*.json; do
  jq -e . "$evidenceFile" >/dev/null
done

echo "publishing sanitized Terraform evidence"
for evidenceFile in "$evidenceDir"/*.json; do
  basename "$evidenceFile"
done | sort
