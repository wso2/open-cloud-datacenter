#!/bin/sh
set -eu

cd "/workspace/runs/$WORKFLOW_UID/terraform"
terraform init -input=false -no-color -lockfile=readonly
