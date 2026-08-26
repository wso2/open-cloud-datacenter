#!/bin/sh
set -eu

runDir="/workspace/runs/$WORKFLOW_UID"
[ -s "$runDir/tenant-space.tfplan" ] || {
  echo "saved Terraform plan is missing" >&2
  exit 1
}
cd "$runDir/terraform"
terraform apply \
  -input=false \
  -no-color \
  "$runDir/tenant-space.tfplan"
