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
#   --no-git                Write into your checkout and leave committing and
#                           pushing to you. By default both subcommands build
#                           their files in a temporary worktree of the branch
#                           Flux reads, commit there and push - your own
#                           checkout is never touched. Pass --no-git to review
#                           the diff or batch the push with other work.
#   --consumer-dir <path>   Consumer repo. Default: git toplevel of $PWD, when
#                           that repo has an environments/ directory.
#   --env-name <name>       Environment under environments/. Default: the only
#                           one carrying a flux/ overlay; required if several.
#   --remote <name>         Git remote to push to. Default: derived from the
#                           cluster's GitRepository/flux-system URL, so the seal
#                           lands where Flux actually reads from.
#   --branch <name>         Branch to push to. Default: derived the same way.
#                           Your checked-out branch is NOT used - sealing while
#                           standing on main would otherwise push secrets to a
#                           branch Flux never reads.
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
git_remote=""
git_branch=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --consumer-dir) consumer_dir="$2"; shift 2 ;;
    --env-name)     env_name="$2"; shift 2 ;;
    --force)        force=true; shift ;;
    --no-git)       no_git=true; shift ;;
    --remote)       git_remote="$2"; shift 2 ;;
    --branch)       git_branch="$2"; shift 2 ;;
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

# ── Infer consumer-dir and env-name ──────────────────────────────────────────
# Both are discoverable from where the operator is standing, and making them
# mandatory meant every invocation carried two paths that the repo layout
# already determines. Explicit flags still win.
#
# consumer_dir: the git toplevel of $PWD, provided it looks like a consumer
# repo (has an environments/ directory). Anything else is ambiguous, so ask.
if [[ -z "$consumer_dir" ]]; then
  _top="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  if [[ -n "$_top" && -d "$_top/environments" ]]; then
    consumer_dir="$_top"
    echo "  ✓ consumer-dir  $consumer_dir (git toplevel)"
  fi
fi
[[ -z "$consumer_dir" ]] && {
  echo "✗ --consumer-dir not given and \$PWD is not inside a consumer repo" >&2
  echo "  (expected a git repo containing an environments/ directory)" >&2
  exit 4
}

# env_name: the single directory under environments/ that carries a flux/
# overlay. With several, the operator has to say which - guessing would seal
# secrets into the wrong environment.
if [[ -z "$env_name" ]]; then
  _envs=()
  for _d in "$consumer_dir"/environments/*/; do
    [[ -d "$_d/flux" ]] && _envs+=("$(basename "$_d")")
  done
  if [[ ${#_envs[@]} -eq 1 ]]; then
    env_name="${_envs[0]}"
    echo "  ✓ env-name      $env_name (only environment with a flux/ overlay)"
  elif [[ ${#_envs[@]} -gt 1 ]]; then
    echo "✗ --env-name not given and several environments have a flux/ overlay:" >&2
    printf '    %s\n' "${_envs[@]}" >&2
    echo "  Pass --env-name <name>; guessing would seal into the wrong one." >&2
    exit 4
  fi
fi
[[ -z "$env_name" ]] && {
  echo "✗ --env-name not given and no environment under $consumer_dir/environments has a flux/ overlay" >&2
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
echo "  Target:        environments/$env_name/flux"
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

# ── Resolve where Flux actually reads from ───────────────────────────────────
# The cluster is the source of truth. GitRepository/flux-system states the exact
# URL and branch Flux reconciles; deriving from it means the seal cannot land on
# the wrong branch because of whatever the operator happens to have checked out.
# Explicit --remote/--branch still win, for disconnected use.
resolve_flux_target() {
  [[ -n "$git_remote" && -n "$git_branch" ]] && return 0

  local url branch
  url="$(kubectl get gitrepository -n flux-system flux-system -o jsonpath='{.spec.url}' 2>/dev/null || true)"
  branch="$(kubectl get gitrepository -n flux-system flux-system -o jsonpath='{.spec.ref.branch}' 2>/dev/null || true)"

  if [[ -z "$url" || -z "$branch" ]]; then
    echo "  ✗ cannot read GitRepository/flux-system from the current context." >&2
    echo "    Flux must be bootstrapped first, or pass --remote and --branch." >&2
    return 1
  fi
  [[ -z "$git_branch" ]] && git_branch="$branch"

  if [[ -z "$git_remote" ]]; then
    # Match the GitRepository URL against configured remotes, comparing on
    # owner/repo so ssh://git@ssh.github.com:443/o/r.git and
    # https://github.com/o/r.git are recognised as the same repository.
    local want me name
    want="$(printf '%s' "$url" | sed -E 's#^(https?://|ssh://)?(git@)?[^/]+[:/]##; s#\.git$##' | tr 'A-Z' 'a-z')"
    while read -r name; do
      [[ -z "$name" ]] && continue
      me="$(git -C "$consumer_dir" remote get-url "$name" 2>/dev/null | sed -E 's#^(https?://|ssh://)?(git@)?[^/]+[:/]##; s#\.git$##' | tr 'A-Z' 'a-z')"
      [[ "$me" == "$want" ]] && { git_remote="$name"; break; }
    done < <(git -C "$consumer_dir" remote)
  fi

  if [[ -z "$git_remote" ]]; then
    echo "  ✗ no git remote in $consumer_dir points at the repository Flux reads:" >&2
    echo "      $url" >&2
    echo "    add one, or pass --remote <name>." >&2
    return 1
  fi
  echo "  ✓ Flux reads $git_remote/$git_branch (from GitRepository/flux-system)"
}

# ── Build the output inside the branch Flux reads ────────────────────────────
# Both subcommands edit files that already exist on that branch (seal rewrites
# platform-overlay/kustomization.yaml), so they must start from ITS copy. The
# operator's checkout can hold an older overlay - on another branch it usually
# does - and a seal built on top of that would revert the live one when pushed.
#
# So everything is written into a throwaway worktree of <remote>/<branch>,
# committed there and pushed from there. The operator's branch, working tree
# and any rebase or merge in progress are never touched, and a failed run
# leaves nothing behind: re-running starts again from the remote tip.
work_tree=""
cleanup_work_tree() {
  [[ -n "$work_tree" ]] || return 0
  git -C "$consumer_dir" worktree remove --force "$work_tree" >/dev/null 2>&1 || true
}
trap cleanup_work_tree EXIT

prepare_work_tree() {
  $no_git && return 0

  echo "── git: target branch ──"
  resolve_flux_target || exit 1

  # Exit code 2 from ls-remote --exit-code means "reachable, no such branch".
  # Anything else non-zero is a connectivity or auth failure worth surfacing.
  local base rc=0
  git -C "$consumer_dir" ls-remote --exit-code --heads "$git_remote" "$git_branch" >/dev/null 2>&1 || rc=$?
  case "$rc" in
    0)
      git -C "$consumer_dir" fetch -q "$git_remote" "$git_branch"
      base="$(git -C "$consumer_dir" rev-parse FETCH_HEAD)"
      ;;
    2)
      base="$(git -C "$consumer_dir" rev-parse HEAD)"
      echo "  ! $git_remote/$git_branch does not exist yet; it will be created from your HEAD"
      ;;
    *)
      echo "  ✗ cannot reach $git_remote ($(git -C "$consumer_dir" remote get-url "$git_remote"))." >&2
      echo "    Check your access to it, or pass --no-git and push yourself." >&2
      exit 1
      ;;
  esac

  work_tree="$(mktemp -d)"
  git -C "$consumer_dir" worktree add -q --detach "$work_tree" "$base"
  target_dir="$work_tree/environments/$env_name/flux"
  overlay_dir="$target_dir/platform-overlay"
  echo "  ✓ writing into $git_remote/$git_branch @ ${base:0:7} (temporary worktree; your checkout is untouched)"
  echo
}

# ── Commit + push ────────────────────────────────────────────────────────────
# Called at the end of both init and seal. The explicit HEAD:refs/heads/<branch>
# refspec needs no local branch, tracking ref or push.default setting.
git_commit_push() {
  local path="$1" message="$2"

  echo
  echo "── git: commit + push ──"

  if $no_git; then
    echo "  --no-git given; do this yourself:"
    echo "    cd $consumer_dir"
    echo "    git add $path"
    echo "    git commit -m \"$message\" -- $path"
    echo "    git push <remote> HEAD:refs/heads/<branch Flux reads>"
    return 0
  fi

  (
    cd "$work_tree"
    git add -A -- "$path"
    if git diff --cached --quiet; then
      echo "  ✓ nothing changed under $path; nothing to push"
      exit 0
    fi
    git commit -q -m "$message"
    echo "  ✓ committed: $message"

    # A rejected push means the branch moved after we read it, most often
    # Flux image automation bumping a tag. Our commit exists only in this
    # worktree, so replaying it onto the new tip rewrites nothing shared.
    local out=""
    for _ in 1 2 3; do
      if out="$(git push -q "$git_remote" "HEAD:refs/heads/$git_branch" 2>&1)"; then
        echo "  ✓ pushed to $git_remote/$git_branch"
        exit 0
      fi
      [[ "$out" == *"[rejected]"* ]] || break
      echo "  ! push rejected ($git_remote/$git_branch moved); replaying onto its new tip"
      git fetch -q "$git_remote" "$git_branch" 2>/dev/null || break
      if ! git rebase -q FETCH_HEAD >/dev/null 2>&1; then
        git rebase --abort >/dev/null 2>&1 || true
        out="replaying onto the new tip of $git_remote/$git_branch conflicted"
        break
      fi
    done
    echo "  ✗ push to $git_remote/$git_branch failed:" >&2
    printf '%s\n' "$out" | sed 's/^/      /' >&2
    echo "    Nothing was pushed and your checkout is untouched. Re-run once the" >&2
    echo "    cause is fixed; it starts again from the branch's latest tip." >&2
    exit 1
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

  # dc-runner retired (no arc/base in flux/platform) — no runner_github_* needed.
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
  echo "  Wrote: environments/$env_name/flux/"

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

  # Hostnames come from the dc-controlplane layer. They are only needed here to
  # put the right SANs on the ingress certificate, but they were a prompt with
  # no discoverable source - the fallback below greps the rendered overlay, and
  # only matches if the hostnames happen to start "dcapi." / "cloud.".
  auto_or_ask dcapi_hostname   "dc-api hostname (for TLS SAN)"   "dc-controlplane" "dcapi_hostname"    "${default_dcapi:-}"   "$HOSTNAME_RE"
  auto_or_ask cloudui_hostname "cloud-ui hostname (for TLS SAN)" "dc-controlplane" "cloud_ui_hostname" "${default_cloudui:-}" "$HOSTNAME_RE"

  # These come straight from terraform outputs — no typing.
  auto_or_ask_secret rancher_admin_token    "Rancher admin token"               "rancher-auth"   "admin_token"
  auto_or_ask        harvester_cred_id      "Harvester cloud_credential_id"     "management"     "cloud_credential_id"
  auto_or_ask_secret bff_client_id          "Asgardeo BFF client_id"            "asgardeo-auth"  "bff_client_id"
  auto_or_ask_secret bff_client_secret      "Asgardeo BFF client_secret"        "asgardeo-auth"  "bff_client_secret"
  auto_or_ask        rancher_oidc_client_id "Asgardeo rancher-sso client_id"    "asgardeo-auth"  "client_id"

  # IdP directory M2M credential (optional feature) — TF-only, never a
  # prompt: when the asgardeo-auth layer registers no directory app, the
  # keys are sealed empty and the feature stays dark. dc-api fails fast on
  # a HALF-configured DCAPI_IDP_* set, so if the overlay patches the
  # SCIM/token URLs into dc-api-config these creds must resolve here.
  idp_client_id="$(tf_get asgardeo-auth directory_client_id)"
  idp_client_secret="$(tf_get asgardeo-auth directory_client_secret)"
  if [[ -n "$idp_client_id" && -n "$idp_client_secret" ]]; then
    echo "  ✓ IdP directory client_id/client_secret  ← from TF (asgardeo-auth.directory_client_id/_secret) [sensitive, not displayed]"
  else
    echo "  –  IdP directory credential not in asgardeo-auth TF outputs — sealing empty (directory feature stays dark)"
  fi

  # Cluster-only / operator-only — stay prompts.

  # Harvester kubeconfig: the bootstrap layer already takes this as an input, so
  # read it back rather than asking. The output is relative to THAT layer's
  # directory (it is how the harvester provider resolves it), so join it there
  # rather than against our cwd.
  echo
  harvester_kubeconfig_path=""
  local _bs_dir _bs_rel
  _bs_dir="$(resolve_layer_dir bootstrap)"
  _bs_rel="$(tf_get bootstrap harvester_kubeconfig_path)"
  if [[ -n "$_bs_dir" && -n "$_bs_rel" ]]; then
    case "$_bs_rel" in
      /*) harvester_kubeconfig_path="$_bs_rel" ;;
       *) harvester_kubeconfig_path="$_bs_dir/${_bs_rel#./}" ;;
    esac
    # -f as well as -r: a readable DIRECTORY passes -r, and the kubectl
    # --from-file below would then fail with something far less obvious than
    # the prompt this is meant to avoid.
    if [[ -f "$harvester_kubeconfig_path" && -r "$harvester_kubeconfig_path" ]]; then
      echo "  ✓ Harvester kubeconfig  ← from TF (bootstrap.harvester_kubeconfig_path): $harvester_kubeconfig_path"
    else
      echo "  !  bootstrap.harvester_kubeconfig_path points at $harvester_kubeconfig_path, which is not readable"
      harvester_kubeconfig_path=""
    fi
  fi
  if [[ -z "$harvester_kubeconfig_path" ]]; then
    echo "  ℹ  Harvester kubeconfig: the file you used in TF for the 00-bootstrap"
    echo "     layer. Typically: <consumer-repo>/environments/<env>/00-bootstrap/harvester.kubeconfig"
    ask_file harvester_kubeconfig_path "Path to Harvester kubeconfig file"
  fi


  # Runner PAT prompt removed — dc-runner retired (no arc/base in flux/platform).

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
  trap 'rm -f "$cert_pem"; cleanup_work_tree' EXIT
  kubeseal \
    --controller-namespace=sealed-secrets \
    --controller-name=sealed-secrets-controller \
    --fetch-cert > "$cert_pem"
  echo "  ✓ fetched sealed-secrets controller cert"

  # Re-sealing a LIVE env must not rotate the Postgres password: the DB
  # initialised with the existing one, and a rotated Secret breaks dc-api's
  # DB auth until an operator ALTERs the role. Reuse the live values when
  # the cluster has them; only a fresh env generates new ones. (The BFF
  # session secret is preserved too so a re-seal doesn't log everyone out.)
  postgres_password="$(kubectl get secret -n dc-system dc-postgres-secret -o jsonpath='{.data.password}' 2>/dev/null | base64 -d || true)"
  if [[ -n "$postgres_password" ]]; then
    echo "  ✓ Postgres password  ← live cluster Secret (preserved, not rotated)"
  else
    postgres_password="$(openssl rand -hex 12)"
  fi
  bff_session_secret="$(kubectl get secret -n dc-system dc-api-secrets -o jsonpath='{.data.DCAPI_BFF_SESSION_SECRET}' 2>/dev/null | base64 -d || true)"
  if [[ -n "$bff_session_secret" ]]; then
    echo "  ✓ BFF session secret ← live cluster Secret (preserved)"
  else
    bff_session_secret="$(openssl rand -base64 32)"
  fi
  # Audience = every client whose tokens dc-api must accept. Single source of
  # truth: the asgardeo-auth layer's dc_api_oidc_audiences output, which joins
  # every TF-managed client (rancher-sso, cloud-ui SPA, cloud-ui BFF, dcctl).
  # Sourcing it here (instead of hand-assembling) is what keeps a from-scratch
  # bootstrap and an ad-hoc re-seal byte-identical, and stops a client from
  # being silently dropped (which is how dcctl fell out of the list before).
  # Fallback for consumers whose asgardeo-auth predates the output. It covers
  # only rancher-sso and the cloud-ui BFF — not dcctl, and not the standalone
  # cloud-ui SPA client, which no longer exists: the BFF now fronts the web
  # console, so there is no separate SPA client to admit. Add the
  # dc_api_oidc_audiences output rather than extending this list.
  oidc_audience="$(tf_get asgardeo-auth dc_api_oidc_audiences)"
  if [[ -z "$oidc_audience" ]]; then
    oidc_audience="$rancher_oidc_client_id,$bff_client_id"
  fi

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
       --from-literal=DCAPI_IDP_CLIENT_ID="$idp_client_id" \
       --from-literal=DCAPI_IDP_CLIENT_SECRET="$idp_client_secret" \
       --from-file=DCAPI_HARVESTER_KUBECONFIG="$harvester_kubeconfig_path"

  # keyvault-system pull secret removed — KVI runs on Harvester
  # (TF-deployed), not on dcapi-controlplane.

  # GitHub runner PAT seal removed — dc-runner retired (no arc/base in flux/platform).

  # dc-api-tls
  if [[ "$tls_source" == "b" ]]; then
    tls_crt_pem="$(cat "$tls_crt_path")"
    tls_key_pem="$(cat "$tls_key_path")"
  else
    echo "  generating self-signed cert (1yr) for $dcapi_hostname + $cloudui_hostname"
    tls_workdir="$(mktemp -d)"
    trap 'rm -rf "$tls_workdir" "$cert_pem"; cleanup_work_tree' EXIT
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
  echo "  Wrote: environments/$env_name/flux/platform-overlay/sealed-*.yaml + updated kustomization.yaml"

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
prepare_work_tree

# seal needs init to have run first - checked against the branch Flux reads.
if [[ "$subcommand" == "seal" && ! -f "$overlay_dir/kustomization.yaml" ]]; then
  echo "✗ environments/$env_name/flux/platform-overlay/kustomization.yaml not found${work_tree:+ on $git_remote/$git_branch}" >&2
  echo "  run \`$0 init\` first" >&2
  exit 1
fi

case "$subcommand" in
  init) cmd_init ;;
  seal) cmd_seal ;;
esac
