#!/usr/bin/env bash
#
# init-flux.sh — two-subcommand wizard for the consumer Flux overlay.
#
# Usage:
#   init-flux.sh init --consumer-dir <path> --env-name <env> [--force]
#   init-flux.sh seal --consumer-dir <path> --env-name <env> [--force]
#
# Subcommands:
#   init   Render the overlay template into <consumer>/environments/<env>/flux/.
#          Prompts for hostnames, IdP org, GHCR org, VPC values, initial
#          image tags. Generates sources.yaml, infrastructure.yaml,
#          platform.yaml, image-update-automation.yaml, README.md,
#          platform-overlay/kustomization.yaml (with sealed-secret refs
#          commented out). Does NOT contact the cluster. Run this first.
#
#   seal   Add env-specific sealed secrets to the overlay. Requires the
#          sealed-secrets controller to be running on the cluster pointed
#          at by your current kubectl context (i.e. Flux's infrastructure
#          Kustomization must be Ready). Prompts for secret values,
#          generates a TLS cert (or uses BYO), runs kubeseal against the
#          live controller, drops platform-overlay/sealed-*.yaml, and
#          uncomments their references in kustomization.yaml. Run AFTER
#          init + TF apply 06-flux-bootstrap + Flux's infrastructure
#          Kustomization is Ready.
#
# Flags (both subcommands):
#   --consumer-dir <path>   Path to your consumer repo root.
#   --env-name <name>       Env slug. Target dir is
#                           <consumer>/environments/<env>/flux/.
#   --force                 Overwrite existing files.
#                           init: regenerates the overlay (preserves
#                                 flux-system/ + sealed-*.yaml from prior runs)
#                           seal: regenerates sealed-*.yaml (preserves the rest)
#   --no-git                Don't auto stage/commit/push the generated files.
#                           By default both subcommands commit + push for you
#                           to whatever branch your consumer repo is on, using
#                           the upstream-safe `git push -u origin HEAD:<branch>`
#                           form. Pass --no-git if you want to review the diff
#                           or batch the push with other work.
#   --remote <name>         Git remote to push to. Default: origin.
#   -h, --help              Show this help.

set -euo pipefail

# ── arg parse ────────────────────────────────────────────────────────────────
[[ $# -lt 1 ]] && {
  sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//;$d'
  exit 1
}

subcommand=""
case "$1" in
  init|seal)  subcommand="$1"; shift ;;
  -h|--help)
    sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//;$d'
    exit 0
    ;;
  *)
    echo "Unknown subcommand: $1" >&2
    echo "Usage: $0 <init|seal> --consumer-dir <path> --env-name <env> [--force]" >&2
    exit 4
    ;;
esac

consumer_dir=""
env_name=""
force=false
no_git=false
git_remote="origin"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --consumer-dir) consumer_dir="$2"; shift 2 ;;
    --env-name)     env_name="$2"; shift 2 ;;
    --force)        force=true; shift ;;
    --no-git)       no_git=true; shift ;;
    --remote)       git_remote="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//;$d'
      exit 0
      ;;
    *)
      echo "unknown flag: $1" >&2
      exit 4
      ;;
  esac
done

[[ -z "$consumer_dir" || -z "$env_name" ]] && {
  echo "missing required: --consumer-dir + --env-name (use --help)" >&2
  exit 4
}

# Resolve paths
ocd_root="$(cd "$(dirname "$0")/.." && pwd)"
template_dir="$ocd_root/examples/consumer/flux"
target_dir="$consumer_dir/environments/$env_name/flux"
overlay_dir="$target_dir/platform-overlay"

[[ ! -d "$template_dir" ]] && { echo "✗ template not found: $template_dir" >&2; exit 1; }
[[ ! -d "$consumer_dir" ]] && { echo "✗ consumer dir not found: $consumer_dir" >&2; exit 1; }

echo "── init-flux ($subcommand) ──────────────────────────────────"
echo "  OCD template:  $template_dir"
echo "  Consumer:      $consumer_dir"
echo "  Target:        $target_dir"
echo

# ── prereq checks ────────────────────────────────────────────────────────────
# Both subcommands need local tools. seal additionally needs the cluster.
prereq_check() {
  local fail=0
  echo "── prereq check ──"

  for cmd in sed awk; do
    if command -v "$cmd" >/dev/null 2>&1; then
      echo "  ✓ $cmd"
    else
      echo "  ✗ $cmd not on PATH" >&2; fail=1
    fi
  done

  if [[ "$subcommand" == "seal" ]]; then
    for cmd in kubectl kubeseal openssl; do
      if command -v "$cmd" >/dev/null 2>&1; then
        echo "  ✓ $cmd"
      else
        echo "  ✗ $cmd not on PATH" >&2; fail=1
      fi
    done

    if command -v kubectl >/dev/null 2>&1; then
      local current_ctx
      current_ctx="$(kubectl config current-context 2>/dev/null || true)"
      if [[ -z "$current_ctx" ]]; then
        echo "  ✗ no current kubectl context (run: kubectl config use-context <dcapi-controlplane-context>)" >&2
        fail=1
      else
        echo "  ✓ kubectl context: $current_ctx"
        if ! kubectl get namespace sealed-secrets >/dev/null 2>&1; then
          echo "  ✗ namespace 'sealed-secrets' not found in context '$current_ctx'." >&2
          echo "    Is this the dcapi-controlplane cluster? Has Flux's infrastructure Kustomization finished?" >&2
          echo "    Try: kubectl --context=$current_ctx get kustomization -A" >&2
          fail=1
        elif ! kubectl --namespace=sealed-secrets get deploy sealed-secrets-controller >/dev/null 2>&1; then
          echo "  ✗ sealed-secrets-controller Deployment not found in 'sealed-secrets' ns." >&2
          fail=1
        else
          local ready_replicas
          ready_replicas=$(kubectl --namespace=sealed-secrets get deploy sealed-secrets-controller -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
          if [[ "${ready_replicas:-0}" -lt 1 ]]; then
            echo "  ✗ sealed-secrets-controller has $ready_replicas ready replicas — wait for it to come up." >&2
            fail=1
          else
            echo "  ✓ sealed-secrets-controller is Ready ($ready_replicas replica(s))"
          fi
        fi
      fi
    fi
  fi

  # seal additionally needs init to have run first
  if [[ "$subcommand" == "seal" ]]; then
    if [[ ! -f "$overlay_dir/kustomization.yaml" ]]; then
      echo "  ✗ $overlay_dir/kustomization.yaml not found — run \`$0 init …\` first" >&2
      fail=1
    fi
  fi

  if [[ "$fail" -ne 0 ]]; then
    echo; echo "✗ Prereq check failed — fix the items above and re-run." >&2
    exit 1
  fi
  echo
}

prereq_check

# ── prompt helpers ───────────────────────────────────────────────────────────
ask() {
  local var="$1" prompt="$2" default="${3:-}" pattern="${4:-}"
  local input=""
  while true; do
    if [[ -n "$default" ]]; then
      read -r -p "  $prompt [$default]: " input
      input="${input:-$default}"
    else
      read -r -p "  $prompt: " input
    fi
    if [[ -z "$input" ]]; then
      echo "  ✗ value cannot be empty, try again" >&2
      continue
    fi
    if [[ -n "$pattern" && ! "$input" =~ $pattern ]]; then
      echo "  ✗ value doesn't match expected pattern ($pattern), try again" >&2
      continue
    fi
    break
  done
  printf -v "$var" '%s' "$input"
}

ask_secret() {
  local var="$1" prompt="$2"
  local input=""
  while [[ -z "$input" ]]; do
    read -r -s -p "  $prompt: " input
    echo
    [[ -z "$input" ]] && echo "  ✗ value cannot be empty, try again" >&2
  done
  printf -v "$var" '%s' "$input"
}

ask_file() {
  local var="$1" prompt="$2" default="${3:-}"
  local input=""
  while true; do
    if [[ -n "$default" ]]; then
      read -r -p "  $prompt [$default]: " input
      input="${input:-$default}"
    else
      read -r -p "  $prompt: " input
    fi
    if [[ -z "$input" ]]; then
      echo "  ✗ value cannot be empty, try again" >&2; continue
    fi
    case "$input" in
      "~"|"~/"*) input="${HOME}${input#\~}" ;;
      "~"*)      input="$(eval echo "${input%%/*}")${input#*/}" ;;
    esac
    if [[ -f "$input" ]]; then
      break
    fi
    echo "  ✗ file not found: $input — try again" >&2
  done
  printf -v "$var" '%s' "$input"
}

# Validators reused below.
HOSTNAME_RE='^[a-zA-Z0-9.-]+$'
IP_RE='^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'
CIDR_RE='^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$'
SLUG_RE='^[a-zA-Z0-9_-]+$'

# ── tf output auto-resolve ───────────────────────────────────────────────────
# Pull values straight from existing terraform outputs so the operator doesn't
# re-type things terraform already knows (admin_token, BFF client_id, etc.).
# Falls back to interactive prompt when the output isn't there.

# resolve_layer_dir: maps bare layer name ("rancher-auth") to actual dir path
# under $consumer_dir/environments/$env_name/. Matches tf.sh behaviour.
resolve_layer_dir() {
  local layer="$1"
  local env_dir="$consumer_dir/environments/$env_name"
  [[ -d "$env_dir/$layer" ]] && { echo "$env_dir/$layer"; return; }
  find "$env_dir" -maxdepth 1 -type d -name "*-${layer}" 2>/dev/null | sort | head -1
}

# tf_get: echo the raw value of an output from a layer; empty if missing.
tf_get() {
  local layer="$1" out_name="$2"
  local dir
  dir="$(resolve_layer_dir "$layer")"
  [[ -z "$dir" ]] && return
  (cd "$dir" && terraform output -raw "$out_name" 2>/dev/null) || true
}

# auto_or_ask: use terraform output if present, else prompt.
auto_or_ask() {
  local var="$1" prompt="$2" layer="$3" out_name="$4" default="${5:-}" pattern="${6:-}"
  local tf_val
  tf_val="$(tf_get "$layer" "$out_name")"
  if [[ -n "$tf_val" ]]; then
    printf -v "$var" '%s' "$tf_val"
    echo "  ✓ $prompt  ← from TF ($layer.$out_name)"
    return
  fi
  ask "$var" "$prompt" "$default" "$pattern"
}

# auto_or_ask_secret: same but masks the display (never echoes the value).
auto_or_ask_secret() {
  local var="$1" prompt="$2" layer="$3" out_name="$4"
  local tf_val
  tf_val="$(tf_get "$layer" "$out_name")"
  if [[ -n "$tf_val" ]]; then
    printf -v "$var" '%s' "$tf_val"
    echo "  ✓ $prompt  ← from TF ($layer.$out_name) [sensitive, not displayed]"
    return
  fi
  ask_secret "$var" "$prompt"
}

# ── git helper ───────────────────────────────────────────────────────────────
# Stage + commit + push the wizard's output. Called at the end of both init
# and seal so the operator doesn't have to remember the right invocation
# (and so we avoid the upstream-branch-name trap that `git push` falls into
# when push.default = simple sees a non-matching upstream).
#
# Pushes to whatever branch the consumer repo is currently on — never
# hardcoded. Uses `git push -u origin HEAD:refs/heads/<branch>` so:
#   - the first push from a fresh checkout sets the tracking ref (-u)
#   - subsequent pushes don't depend on whatever push.default is configured
#   - it works regardless of whether the upstream branch name matches
#
# Opt out with --no-git on the script invocation (the script then prints
# the exact commands to run by hand).
git_commit_push() {
  local path="$1" message="$2"

  echo
  echo "── git: stage + commit + push ──"

  (
    cd "$consumer_dir"

    if $no_git; then
      echo "  --no-git given; do this yourself:"
      echo "    cd $consumer_dir"
      echo "    git add $path"
      echo "    git commit -m \"$message\""
      echo "    git push -u $git_remote HEAD"
      return 0
    fi

    # Must be on a branch (not detached HEAD) for the push to know where
    # to send things.
    local branch
    if ! branch=$(git symbolic-ref --short HEAD 2>/dev/null); then
      echo "  ✗ HEAD is detached — can't auto-push" >&2
      echo "    check out a branch and run by hand:" >&2
      echo "      cd $consumer_dir" >&2
      echo "      git add $path" >&2
      echo "      git commit -m \"$message\"" >&2
      echo "      git push -u $git_remote HEAD:refs/heads/<branch>" >&2
      return 1
    fi

    # Verify the remote exists.
    if ! git remote get-url "$git_remote" >/dev/null 2>&1; then
      echo "  ✗ git remote '$git_remote' not configured in $consumer_dir" >&2
      echo "    add it with: git remote add $git_remote <url>" >&2
      echo "    or re-run with --remote <name> --no-git to do it yourself" >&2
      return 1
    fi

    # Stage + commit any new changes under $path.
    if [[ -n "$(git status --porcelain -- "$path" 2>/dev/null)" ]]; then
      git add -- "$path"
      if git diff --cached --quiet -- "$path"; then
        echo "  ✓ working tree under $path matches index — nothing to commit"
      else
        git commit -m "$message" >/dev/null
        echo "  ✓ committed: $message"
      fi
    else
      echo "  ✓ no working-tree changes under $path"
    fi

    # Now check whether local is ahead of remote. This matters even when
    # the wizard didn't make changes this run — a prior run may have
    # committed locally but the operator never pushed. Flux then
    # reconciles a stale remote SHA that's missing the overlay, and
    # nothing deploys. We fetch the remote ref so the ahead-count is
    # accurate (silently — a failed fetch shouldn't break the wizard).
    git fetch "$git_remote" "$branch" >/dev/null 2>&1 || true

    local ahead
    if git rev-parse --verify --quiet "$git_remote/$branch" >/dev/null; then
      ahead=$(git rev-list --count "$git_remote/$branch..HEAD" 2>/dev/null || echo 0)
    else
      ahead="new-branch"
    fi

    # Helper: try `git push`; if it fails because remote moved (fast-
    # forward conflict — most often because flux_bootstrap_git's
    # auto-commits landed between the operator's last fetch and now),
    # auto-rebase onto the remote and try once more. Real conflicts
    # (e.g. the operator hand-edited something that flux_bootstrap_git
    # also touched) abort the rebase and tell the operator what to do.
    try_push() {
      if git push -u "$git_remote" "HEAD:refs/heads/$branch" >/dev/null 2>&1; then
        return 0
      fi
      echo "  ! push rejected (remote moved); auto-rebasing onto $git_remote/$branch"
      git fetch "$git_remote" "$branch" >/dev/null 2>&1 || true
      if ! git pull --rebase "$git_remote" "$branch" >/dev/null 2>&1; then
        git rebase --abort >/dev/null 2>&1 || true
        echo "  ✗ auto-rebase hit a conflict — needs manual resolution:" >&2
        echo "      cd $consumer_dir" >&2
        echo "      git pull --rebase $git_remote $branch" >&2
        echo "      # resolve conflicts, then:" >&2
        echo "      git push $git_remote HEAD:refs/heads/$branch" >&2
        return 1
      fi
      if git push -u "$git_remote" "HEAD:refs/heads/$branch" >/dev/null 2>&1; then
        return 0
      fi
      echo "  ✗ push still failed after rebase" >&2
      return 1
    }

    case "$ahead" in
      0)
        echo "  ✓ $git_remote/$branch already at HEAD — nothing to push"
        ;;
      new-branch)
        if try_push; then
          echo "  ✓ pushed (created $git_remote/$branch)"
        else
          return 1
        fi
        ;;
      *)
        # Explicit refspec form. Bypasses push.default surprises (the
        # 'upstream branch of your current branch does not match the
        # name of your current branch' error happens when
        # push.default=simple sees a tracking ref with a different name
        # than local HEAD). HEAD:refs/heads/X tells git unambiguously:
        # push my current commit to branch X on origin.
        if try_push; then
          # Re-count ahead after potential rebase + push (where the
          # original local SHA may have been replayed).
          local pushed
          pushed=$(git rev-list --count "$git_remote/$branch@{1}..$git_remote/$branch" 2>/dev/null || echo "$ahead")
          echo "  ✓ pushed $pushed commit(s) to $git_remote/$branch"
        else
          return 1
        fi
        ;;
    esac
  )
}

# ── subcommand: init ─────────────────────────────────────────────────────────
cmd_init() {
  if [[ -e "$overlay_dir/kustomization.yaml" ]]; then
    if ! $force; then
      echo "✗ $target_dir already initialised. Pass --force to regenerate." >&2
      exit 1
    fi
    echo "  ! --force given; overwriting wizard-managed files"
    # Preserve flux-system/ (Flux-managed) and any sealed-*.yaml from prior
    # seal runs (operator may have hand-edited them; safe to leave since
    # they're env-specific and seal will refresh on next seal run).
    rm -f  "$target_dir/sources.yaml" \
           "$target_dir/infrastructure.yaml" \
           "$target_dir/platform.yaml" \
           "$target_dir/image-update-automation.yaml" \
           "$target_dir/README.md" \
           "$target_dir/kustomization.yaml" \
           "$overlay_dir/kustomization.yaml"
  fi

  echo "── values for env: $env_name ──"
  echo "  (values marked ← from TF are auto-resolved; the rest get prompts)"
  echo

  auto_or_ask rancher_hostname    "Rancher hostname"          "bootstrap"        "rancher_hostname"  ""                  "$HOSTNAME_RE"
  auto_or_ask dcapi_hostname      "dc-api hostname"           "dc-controlplane"  "dcapi_hostname"    ""                  "$HOSTNAME_RE"
  auto_or_ask cloudui_hostname    "cloud-ui hostname"         "dc-controlplane"  "cloud_ui_hostname" ""                  "$HOSTNAME_RE"

  default_cookie_domain=".$(echo "$cloudui_hostname" | cut -d. -f2-)"
  auto_or_ask bff_cookie_domain   "BFF cookie domain"         "dc-controlplane"  "bff_cookie_domain" "$default_cookie_domain"

  auto_or_ask asgardeo_org        "Asgardeo org name"         "asgardeo-auth"    "asgardeo_org_name" ""                  "$SLUG_RE"
  auto_or_ask ghcr_org            "GHCR owner/org"            "flux-bootstrap"   "ghcr_org"          ""                  "$SLUG_RE"
  auto_or_ask vpc_external_cidr   "VPC external CIDR"         "dc-controlplane"  "vpc_external_cidr" "192.168.10.0/24"   "$CIDR_RE"
  auto_or_ask vpc_external_gateway "VPC external gateway"     "dc-controlplane"  "vpc_external_gateway" "192.168.10.254" "$IP_RE"
  auto_or_ask ocd_owner           "OCD repo owner"            "flux-bootstrap"   "ocd_owner"         "wso2"
  auto_or_ask ocd_repo            "OCD repo name"             "flux-bootstrap"   "ocd_repo"          "open-cloud-datacenter"
  auto_or_ask ocd_ref             "OCD pin (tag or branch)"   "flux-bootstrap"   "ocd_ref"           "spike/flux-gitops"
  auto_or_ask git_branch          "Consumer-repo branch"      "flux-bootstrap"   "git_branch"        "spike/flux-gitops"

  # Used to render the ARC dc-runner HelmRelease's githubConfigUrl
  # patch. THIS IS THE SOURCE REPO WHERE CI WORKFLOWS LIVE — not the
  # consumer/Flux state repo. ARC registers runners per-repo; a
  # workflow in repo-A whose `runs-on: dc-runner` won't match a
  # runner registered to repo-B.
  #
  # Read from the 06-flux-bootstrap layer's `runner_github_owner` +
  # `runner_github_repo` outputs (consumer sets these in tfvars).
  # Falls back to prompt if those outputs aren't set yet.
  echo
  echo "  ℹ  ARC runner repo: the SOURCE repo where your CI workflows live"
  echo "     (.github/workflows/*.yaml). NOT the consumer/Flux-state repo."
  echo "     Auto-resolved from 06-flux-bootstrap tfvars when set there."
  auto_or_ask runner_github_owner "GitHub owner of the SOURCE repo (where workflows live)" "flux-bootstrap" "runner_github_owner" "" "$SLUG_RE"
  auto_or_ask runner_github_repo  "GitHub repo name of the SOURCE repo"                   "flux-bootstrap" "runner_github_repo"  "" "$SLUG_RE"
  auto_or_ask dc_api_tag          "Initial dc-api image tag"  "flux-bootstrap"   "dc_api_initial_tag"           "latest"
  auto_or_ask cloud_ui_tag        "Initial cloud-ui image tag" "flux-bootstrap"  "cloud_ui_initial_tag"         "latest"
  # keyvault-operator image tag is set by the TF module that deploys
  # KVI to the Harvester cluster (not Flux-managed on this cluster).

  # Tag-vs-branch heuristic for sources.yaml.
  if [[ "$ocd_ref" =~ ^v[0-9]+\.[0-9]+ ]]; then
    ocd_ref_field="tag"
  else
    ocd_ref_field="branch"
  fi

  echo
  echo "── rendering template ──"
  mkdir -p "$overlay_dir"

  subst() {
    local src="$1" dst="$2"
    sed \
      -e "s|CHANGE-ME-rancher-hostname|$rancher_hostname|g" \
      -e "s|CHANGE-ME-dcapi-hostname|$dcapi_hostname|g" \
      -e "s|CHANGE-ME-cloud-ui-hostname|$cloudui_hostname|g" \
      -e "s|CHANGE-ME-asgardeo-org|$asgardeo_org|g" \
      -e "s|CHANGE-ME-org|$ghcr_org|g" \
      -e "s|\\.CHANGE-ME-parent-domain|$bff_cookie_domain|g" \
      -e "s|CHANGE-ME-cidr|$vpc_external_cidr|g" \
      -e "s|CHANGE-ME-gateway|$vpc_external_gateway|g" \
      -e "s|CHANGE-ME-env|$env_name|g" \
      -e "s|CHANGE-ME-dc-api-tag|$dc_api_tag|g" \
      -e "s|CHANGE-ME-cloud-ui-tag|$cloud_ui_tag|g" \
      -e "s|ref=v0\\.9\\.0|ref=$ocd_ref|g" \
      -e "s|github.com/wso2/open-cloud-datacenter|github.com/$ocd_owner/$ocd_repo|g" \
      -e "s|    tag: v0\\.9\\.0|    $ocd_ref_field: $ocd_ref|g" \
      -e "s|CHANGE-ME-github-owner|$runner_github_owner|g" \
      -e "s|CHANGE-ME-github-repo|$runner_github_repo|g" \
      "$src" > "$dst"
    echo "  wrote $dst"
  }

  subst "$template_dir/sources.yaml"          "$target_dir/sources.yaml"
  subst "$template_dir/infrastructure.yaml"   "$target_dir/infrastructure.yaml"
  subst "$template_dir/platform.yaml"         "$target_dir/platform.yaml"
  subst "$template_dir/platform-overlay/kustomization.yaml" "$overlay_dir/kustomization.yaml"

  # Root kustomization.yaml — without this, Flux's root reconcile
  # falls into a fallback mode that recurses into platform-overlay/
  # and tries to apply Ingresses before ingress-nginx is up.
  # No template substitution needed; it's a static file.
  cp "$template_dir/kustomization.yaml" "$target_dir/kustomization.yaml"
  echo "  wrote $target_dir/kustomization.yaml"

  sed \
    -e "s|CHANGE-ME-env|$env_name|g" \
    -e "s|CHANGE-ME-git-branch|$git_branch|g" \
    -e "s|CHANGE-ME-parent-domain|${bff_cookie_domain#.}|g" \
    "$template_dir/image-update-automation.yaml" > "$target_dir/image-update-automation.yaml"
  echo "  wrote $target_dir/image-update-automation.yaml"

  cat > "$target_dir/README.md" <<README
# Flux overlay — $env_name

Generated by \`scripts/init-flux.sh init\` from OCD's
\`examples/consumer/flux/\` template. Flux running in the $env_name
cluster reconciles this path from this repo.

## Layout

- \`sources.yaml\` — Flux GitRepository CRs (OCD + this repo).
- \`infrastructure.yaml\` — Flux Kustomization for the shared add-ons
  (sealed-secrets, cert-manager, ingress-nginx) sourced from OCD.
- \`platform.yaml\` — Flux Kustomization for \`./platform-overlay/\`.
- \`image-update-automation.yaml\` — Flux ImageUpdateAutomation that
  commits image-tag bumps back to this repo.
- \`platform-overlay/kustomization.yaml\` — overlays patches +
  sealed secrets on top of OCD's shared platform.
- \`platform-overlay/sealed-*.yaml\` — env-specific sealed secrets
  generated by \`init-flux.sh seal\` (not present until that runs).

## Regenerating

- Refresh overlay structure (hostnames, image tags, OCD pin):
  \`init-flux.sh init --consumer-dir <repo> --env-name $env_name --force\`
- Re-seal secrets (rotation, new BFF client, GHCR PAT, …):
  \`init-flux.sh seal --consumer-dir <repo> --env-name $env_name --force\`

Each subcommand is independent — refreshing one doesn't touch the other.
README
  echo "  wrote $target_dir/README.md"

  echo
  echo "── done (init) ──────────────────────────────────────────────"
  echo "  Wrote: $target_dir/"

  git_commit_push "environments/$env_name/flux" \
    "Add Flux overlay for $env_name (init phase)"

  echo
  echo "  Flux will reconcile the new revision on its next interval"
  echo "  (~1 min). The first thing to come up is the infrastructure"
  echo "  Kustomization, which installs sealed-secrets + cert-manager"
  echo "  + ingress-nginx via OCD's flux/infrastructure path."
  echo
  echo "  Wait for it:"
  echo "    kubectl get kustomization -n flux-system -w"
  echo "  When 'infrastructure' shows True, run seal:"
  echo "    $0 seal --consumer-dir $consumer_dir --env-name $env_name"
  echo
  echo "  (tf.sh apply 06-flux-bootstrap is a one-time prereq before"
  echo "   the first init — not something to re-run after every init.)"
}

# ── subcommand: seal ─────────────────────────────────────────────────────────
cmd_seal() {
  # Defaults grep'd back from init's rendered files so the operator doesn't
  # have to retype them. Only hostnames are needed at seal time (for the TLS
  # cert SANs) — everything else is pure secret input.
  local default_dcapi default_cloudui
  default_dcapi=$(grep -oE '"dcapi\.[a-zA-Z0-9.-]+"' "$overlay_dir/kustomization.yaml" 2>/dev/null | head -1 | tr -d '"' || true)
  default_cloudui=$(grep -oE '"cloud\.[a-zA-Z0-9.-]+"' "$overlay_dir/kustomization.yaml" 2>/dev/null | head -1 | tr -d '"' || true)

  echo "── secret values (sealed to cluster, never logged) ──"
  echo "  (values marked ← from TF are auto-resolved; the rest get prompts)"
  echo

  # Hostnames: not in TF outputs today (Stage 2 work). Default from rendered
  # files so re-running seal doesn't require retyping.
  ask  dcapi_hostname               "dc-api hostname (for TLS SAN)"      "${default_dcapi:-}"   "$HOSTNAME_RE"
  ask  cloudui_hostname             "cloud-ui hostname (for TLS SAN)"    "${default_cloudui:-}" "$HOSTNAME_RE"

  # GHCR org: not in TF (operator decision). Default grep'd from overlay's
  # ImageRepository patches so the operator doesn't re-type.
  local default_ghcr_org
  default_ghcr_org=$(grep -oE 'ghcr\.io/[a-zA-Z0-9_-]+/dc-api' "$overlay_dir/kustomization.yaml" 2>/dev/null | head -1 | cut -d/ -f2)
  ask  ghcr_org                     "GHCR owner/org (for image-pull dockerconfigjson)"  "${default_ghcr_org:-}"     "$SLUG_RE"

  # These come straight from terraform outputs — no typing.
  auto_or_ask_secret rancher_admin_token    "Rancher admin token"               "rancher-auth"   "admin_token"
  auto_or_ask        harvester_cred_id      "Harvester cloud_credential_id"     "management"     "cloud_credential_id"
  auto_or_ask_secret bff_client_id          "Asgardeo BFF client_id"            "asgardeo-auth"  "bff_client_id"
  auto_or_ask_secret bff_client_secret      "Asgardeo BFF client_secret"        "asgardeo-auth"  "bff_client_secret"
  auto_or_ask        rancher_oidc_client_id "Asgardeo rancher-sso client_id"    "asgardeo-auth"  "client_id"
  auto_or_ask        cloud_ui_client_id     "Asgardeo cloud-ui SPA client_id"   "asgardeo-auth"  "cloud_ui_client_id"

  # Cluster-only / operator-only — stay prompts.

  echo
  echo "  ℹ  Harvester kubeconfig: the file you used in TF for the 00-bootstrap"
  echo "     layer. Typically: <consumer-repo>/environments/<env>/00-bootstrap/harvester.kubeconfig"
  ask_file   harvester_kubeconfig_path "Path to Harvester kubeconfig file"

  echo
  echo "  ℹ  GHCR pull-token: classic GitHub PAT for pulling the consumer's"
  echo "     images from ghcr.io. Generate at:"
  echo "       https://github.com/settings/tokens (classic)  →  scope: read:packages"
  echo "     Used by dc-system + flux-system to pull images."
  ask_secret ghcr_pat                  "GHCR personal-access token (read:packages)"

  echo
  echo "  ℹ  Runner PAT: SEPARATE classic GitHub PAT for the ARC self-hosted runner"
  echo "     to register itself on the consumer repo. Generate at:"
  echo "       https://github.com/settings/tokens (classic)  →  scope: repo"
  echo "     Kept separate from the GHCR PAT for least-privilege: the runner PAT"
  echo "     can register/deregister runners + read issues; the pull PAT only reads packages."
  ask_secret runner_pat                "GitHub PAT for ARC runner registration (repo scope)"

  echo
  echo "  ℹ  Ingress TLS cert (covers both $dcapi_hostname and $cloudui_hostname)"
  echo "     [s]elf-signed: wizard generates a 1-year cert now. Browsers will warn"
  echo "       — fine for internal envs. The cert is stored only as a sealed-secret."
  echo "     [b]yo: provide paths to existing fullchain.pem + privkey.pem (e.g. from"
  echo "       Let's Encrypt or a corporate CA). The cert must list both hostnames"
  echo "       as SANs (subject alt names)."
  ask  tls_source "  source: [s]elf-signed (generated now) | [b]yo (path to existing PEM files)" "s"
  if [[ "$tls_source" == "b" ]]; then
    ask_file tls_crt_path "  Path to TLS cert PEM (full chain)"
    ask_file tls_key_path "  Path to TLS key PEM"
  fi

  if $force; then
    echo
    echo "  ! --force given; overwriting any prior sealed-*.yaml"
    rm -f "$overlay_dir"/sealed-*.yaml
  fi

  echo
  echo "── sealing secrets ──"

  cert_pem="$(mktemp)"
  trap 'rm -f "$cert_pem"' EXIT
  kubeseal \
    --controller-namespace=sealed-secrets \
    --controller-name=sealed-secrets-controller \
    --fetch-cert > "$cert_pem"
  echo "  ✓ fetched sealed-secrets controller cert"

  postgres_password="$(openssl rand -hex 12)"
  bff_session_secret="$(openssl rand -base64 32)"
  oidc_audience="$rancher_oidc_client_id,$bff_client_id,$cloud_ui_client_id"

  seal() {
    local name="$1" namespace="$2"; shift 2
    kubectl create secret generic "$name" \
      --namespace="$namespace" --dry-run=client -o yaml "$@" \
    | kubeseal --cert "$cert_pem" --format yaml --namespace="$namespace" --name="$name" \
    > "$overlay_dir/sealed-$name.yaml"
    echo "  wrote platform-overlay/sealed-$name.yaml"
  }

  seal_dockerconfig() {
    local filename="$1" name="$2" namespace="$3" username="$4" pat="$5"
    kubectl create secret docker-registry "$name" \
      --namespace="$namespace" \
      --docker-server=ghcr.io \
      --docker-username="$username" --docker-password="$pat" \
      --dry-run=client -o yaml \
    | kubeseal --cert "$cert_pem" --format yaml --namespace="$namespace" --name="$name" \
    > "$overlay_dir/$filename"
    echo "  wrote platform-overlay/$filename"
  }

  seal dc-postgres-secret dc-system \
       --from-literal=password="$postgres_password"

  seal dc-api-secrets dc-system \
       --from-literal=DCAPI_DB_URL="postgres://dc_api:${postgres_password}@dc-postgres.dc-system:5432/dc_api?sslmode=disable" \
       --from-literal=DCAPI_OIDC_AUDIENCE="$oidc_audience" \
       --from-literal=DCAPI_RANCHER_TOKEN="$rancher_admin_token" \
       --from-literal=DCAPI_RANCHER_HARVESTER_CREDENTIAL="$harvester_cred_id" \
       --from-literal=DCAPI_OPERATOR_SSH_KEY="" \
       --from-literal=DCAPI_OPERATOR_PASSWORD="" \
       --from-literal=DCAPI_BFF_CLIENT_ID="$bff_client_id" \
       --from-literal=DCAPI_BFF_CLIENT_SECRET="$bff_client_secret" \
       --from-literal=DCAPI_BFF_SESSION_SECRET="$bff_session_secret" \
       --from-file=DCAPI_HARVESTER_KUBECONFIG="$harvester_kubeconfig_path"

  seal_dockerconfig sealed-ghcr-pull-secret.yaml             ghcr-pull-secret dc-system   "$ghcr_org" "$ghcr_pat"
  seal_dockerconfig sealed-ghcr-pull-secret-flux-system.yaml ghcr-pull-secret flux-system "$ghcr_org" "$ghcr_pat"
  # keyvault-system pull secret removed — KVI runs on Harvester
  # (TF-deployed), not on dcapi-controlplane.

  # GitHub runner PAT — read by the ARC runner scale-set HelmRelease in
  # arc-runners. The key name `github_token` matches what the
  # gha-runner-scale-set chart expects when `githubConfigSecret: github-runner-pat`.
  seal github-runner-pat arc-runners \
       --from-literal=github_token="$runner_pat"

  # dc-api-tls
  if [[ "$tls_source" == "b" ]]; then
    tls_crt_pem="$(cat "$tls_crt_path")"
    tls_key_pem="$(cat "$tls_key_path")"
  else
    echo "  generating self-signed cert (1yr) for $dcapi_hostname + $cloudui_hostname"
    tls_workdir="$(mktemp -d)"
    trap 'rm -rf "$tls_workdir" "$cert_pem"' EXIT
    openssl req -x509 -nodes -newkey rsa:4096 \
      -keyout "$tls_workdir/tls.key" -out "$tls_workdir/tls.crt" \
      -days 365 \
      -subj "/CN=$dcapi_hostname/O=Open Cloud Datacenter" \
      -addext "subjectAltName=DNS:$dcapi_hostname,DNS:$cloudui_hostname" \
      2>/dev/null
    tls_crt_pem="$(cat "$tls_workdir/tls.crt")"
    tls_key_pem="$(cat "$tls_workdir/tls.key")"
  fi

  tls_crt_f="$(mktemp)"; printf '%s' "$tls_crt_pem" > "$tls_crt_f"
  tls_key_f="$(mktemp)"; printf '%s' "$tls_key_pem" > "$tls_key_f"
  kubectl create secret tls dc-api-tls \
    --namespace=dc-system \
    --cert="$tls_crt_f" --key="$tls_key_f" \
    --dry-run=client -o yaml \
  | kubeseal --cert "$cert_pem" --format yaml --namespace=dc-system --name=dc-api-tls \
  > "$overlay_dir/sealed-dc-api-tls.yaml"
  rm -f "$tls_crt_f" "$tls_key_f"
  echo "  wrote platform-overlay/sealed-dc-api-tls.yaml"

  # Uncomment sealed-secret references in platform-overlay/kustomization.yaml.
  # Tolerant of either the old "Sealed secrets the wizard drops" comment
  # or the current "Sealed secrets generated by" wording.
  local_ks="$overlay_dir/kustomization.yaml"
  awk '
    /^  # Sealed secrets (the wizard drops|generated by)/ {
      print "  # Sealed secrets generated by scripts/init-flux.sh seal."
      print "  - ./sealed-dc-postgres-secret.yaml"
      print "  - ./sealed-dc-api-secrets.yaml"
      print "  - ./sealed-dc-api-tls.yaml"
      print "  - ./sealed-ghcr-pull-secret.yaml"
      print "  - ./sealed-ghcr-pull-secret-flux-system.yaml"
      print "  - ./sealed-github-runner-pat.yaml"
      skip = 1
      next
    }
    /^[^ ]/ { skip = 0 }
    skip && /^  # / { next }
    skip && /^  - \.\.?\// { next }
    { print }
  ' "$local_ks" > "$local_ks.new"
  mv "$local_ks.new" "$local_ks"
  echo "  ✓ uncommented sealed-secret resources in kustomization.yaml"

  echo
  echo "── done (seal) ──────────────────────────────────────────────"
  echo "  Wrote: $overlay_dir/sealed-*.yaml + updated kustomization.yaml"

  git_commit_push "environments/$env_name/flux" \
    "Seal $env_name secrets"

  echo
  echo "  Watch the platform Kustomization come up:"
  echo "    kubectl get kustomization,pods -A -w"
}

# trigger_initial_image_builds was removed when KVI moved to TF on
# Harvester. dc-api + cloud-ui images already exist in GHCR and
# image-automation handles ongoing bumps; no first-time build needed
# from the wizard. If a future image goes back to needing a one-shot
# kick (e.g. a new Flux-managed app whose image-automation regex
# doesn't match published tags yet), this is the place to re-add it.

# ── dispatch ─────────────────────────────────────────────────────────────────
case "$subcommand" in
  init) cmd_init ;;
  seal) cmd_seal ;;
esac
