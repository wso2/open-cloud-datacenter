#!/bin/sh
set -eu

runDir="/workspace/runs/$WORKFLOW_UID"
evidenceDir="$runDir/evidence/terraform"
planFile="$runDir/tenant-space.tfplan"
cd "$runDir/terraform"
terraform plan \
  -input=false \
  -no-color \
  -out="$planFile"

planSha256="$(sha256sum "$planFile" | awk '{print $1}')"
generatedAt="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
terraform show -json "$planFile" | jq \
  --arg plan_sha256 "$planSha256" \
  --arg generated_at "$generatedAt" '
    def action_category:
      if . == ["no-op"] then "no-op"
      elif (index("create") != null and index("delete") != null) then "replace"
      elif index("create") != null then "create"
      elif index("update") != null then "update"
      elif index("delete") != null then "delete"
      elif index("read") != null then "read"
      else "other"
      end;

    [
      .resource_changes[]?
      | (.change.actions | action_category) as $action
      | select($action != "no-op")
      | {
          module: (.module_address // "root"),
          type,
          name,
          action: $action
        }
    ] as $resources
    | {
        schemaVersion: "testsuite.opencloud.wso2.com/v1alpha1",
        kind: "TerraformPlanSummary",
        generatedAt: $generated_at,
        planSha256: $plan_sha256,
        counts: {
          create: ([$resources[] | select(.action == "create")] | length),
          update: ([$resources[] | select(.action == "update")] | length),
          delete: ([$resources[] | select(.action == "delete")] | length),
          replace: ([$resources[] | select(.action == "replace")] | length),
          read: ([$resources[] | select(.action == "read")] | length),
          other: ([$resources[] | select(.action == "other")] | length)
        },
        resources: $resources
      }
  ' >"$evidenceDir/plan-summary.json"
