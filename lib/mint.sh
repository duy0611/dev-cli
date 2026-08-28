# shellcheck shell=bash
# Credential minting. Runs on the HOST, against your existing sessions, and
# writes short-lived credentials into the instance's creds/ directory.
#
# Two rules hold for every kind:
#
#   1. The container reads a FILE, never an env value. kubectl reads
#      users[].user.tokenFile, gcloud reads CLOUDSDK_AUTH_ACCESS_TOKEN_FILE,
#      aws reads AWS_SHARED_CREDENTIALS_FILE. All three re-read per invocation,
#      so a refresh here lands with no restart and no signal.
#   2. Every value file gets a <kind>.expiry sidecar holding a unix timestamp,
#      written last, so the shim can block before the API does.

: "${DCX_K8S_TOKEN_TTL:=1h}"
: "${DCX_AWS_TTL_SECONDS:=3600}"
: "${DCX_GCP_TTL_SECONDS:=3600}"

# macOS date. BSD syntax, no GNU -d.
dcx_iso_to_epoch() { # <iso8601, e.g. 2026-08-28T09:00:00Z>
  local ts="${1%%.*}"                 # drop fractional seconds if present
  ts="${ts%Z}"
  TZ=UTC date -j -f "%Y-%m-%dT%H:%M:%S" "$ts" +%s 2>/dev/null
}

# --- kubernetes --------------------------------------------------------------

# Cloud-agnostic on purpose: `kubectl create token` is a Kubernetes API call, so
# this is one code path for EKS and GKE alike. The kubeconfig it writes carries
# a bare token and no exec credential plugin, which is why the container needs
# neither `aws eks get-token` nor gke-gcloud-auth-plugin.
dcx_mint_k8s() { # <instance>
  local name="$1" ctx ns sa creds out token exp_iso exp_epoch server ca ca_file
  ctx="$(dcx_meta "$name" '.k8s.context')"
  ns="$(dcx_meta "$name" '.k8s.namespace')"
  sa="$(dcx_meta "$name" '.k8s.serviceaccount')"
  [ -n "$ctx" ] && [ -n "$ns" ] && [ -n "$sa" ] || return 0

  creds="$(dcx_creds_dir "$name")"

  # -o json so the expiry is the API's own answer rather than our arithmetic:
  # the cluster may cap the requested duration well below what we asked for.
  out="$(kubectl --context "$ctx" -n "$ns" create token "$sa" \
           --duration="$DCX_K8S_TOKEN_TTL" -o json)" || return 1
  token="$(printf '%s' "$out" | jq -r '.status.token')"
  exp_iso="$(printf '%s' "$out" | jq -r '.status.expirationTimestamp')"
  exp_epoch="$(dcx_iso_to_epoch "$exp_iso")"
  [ -n "$token" ] && [ -n "$exp_epoch" ] || { warn "k8s: could not parse token response"; return 1; }

  server="$(kubectl config view --raw --minify --context "$ctx" \
              -o jsonpath='{.clusters[0].cluster.server}')"
  ca="$(kubectl config view --raw --minify --context "$ctx" \
              -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
  if [ -z "$ca" ]; then
    # Some contexts reference a CA file instead of inlining it. The container
    # cannot see host paths, so inline it here.
    ca_file="$(kubectl config view --raw --minify --context "$ctx" \
                 -o jsonpath='{.clusters[0].cluster.certificate-authority}')"
    [ -n "$ca_file" ] && [ -f "$ca_file" ] && ca="$(base64 < "$ca_file" | tr -d '\n')" || true
  fi
  [ -n "$server" ] || { warn "k8s: no server URL for context $ctx"; return 1; }

  printf '%s' "$token" | dcx_write_secret "$creds/k8s.token"

  # Built from scratch rather than edited: replacing the user block wholesale is
  # exactly what strips the exec plugin, and a partial edit would leave it.
  cat > "$creds/kubeconfig.yaml" <<YAML
apiVersion: v1
kind: Config
clusters:
- name: dcx
  cluster:
    server: $server
    certificate-authority-data: $ca
users:
- name: dcx
  user:
    tokenFile: /run/dcx-creds/k8s.token
contexts:
- name: dcx
  context:
    cluster: dcx
    user: dcx
    namespace: $ns
current-context: dcx
YAML
  chmod 644 "$creds/kubeconfig.yaml"

  printf '%s\n' "$exp_epoch" > "$creds/k8s.expiry"
  info "minted k8s token for $sa in $ns on $ctx (expires $exp_iso)"
}

# --- gcp ---------------------------------------------------------------------

dcx_mint_gcp() { # <instance>
  local name="$1" project sa creds token exp_epoch
  project="$(dcx_meta "$name" '.gcp.project')"
  sa="$(dcx_meta "$name" '.gcp.serviceaccount')"
  [ -n "$project" ] || return 0

  creds="$(dcx_creds_dir "$name")"

  if [ -n "$sa" ]; then
    token="$(gcloud auth print-access-token --impersonate-service-account="$sa" --lifetime="$DCX_GCP_TTL_SECONDS" 2>&1)" || {
      warn "gcp: impersonation failed: $token"; return 1; }
  else
    token="$(gcloud auth print-access-token --lifetime="$DCX_GCP_TTL_SECONDS" 2>&1)" || {
      warn "gcp: $token"; return 1; }
  fi

  # Google access tokens are capped at 1h unless the org sets
  # constraints/iam.allowServiceAccountCredentialLifetimeExtension, which we
  # assume it does not. gcloud does not report the expiry, so this is computed,
  # not observed — 60s of margin so the shim blocks slightly before the API does.
  exp_epoch=$(( $(date +%s) + 3600 - 60 ))

  printf '%s' "$token" | dcx_write_secret "$creds/gcp.token"
  printf '%s\n' "$exp_epoch" > "$creds/gcp.expiry"

  if [ -n "$sa" ]; then
    info "minted gcp token impersonating $sa (project $project)"
  else
    info "minted gcp token as your own identity (project $project) — NOT privilege-reduced"
  fi
}

# --- aws ---------------------------------------------------------------------

dcx_mint_aws() { # <instance>
  local name="$1" role region creds out exp_epoch
  role="$(dcx_meta "$name" '.aws.role_arn')"
  region="$(dcx_meta "$name" '.aws.region')"
  [ -n "$role" ] || return 0

  creds="$(dcx_creds_dir "$name")"

  out="$(aws sts assume-role \
           --role-arn "$role" \
           --role-session-name "dcx-$name" \
           --duration-seconds "$DCX_AWS_TTL_SECONDS" \
           --output json 2>&1)" || { warn "aws: $out"; return 1; }

  exp_epoch="$(dcx_iso_to_epoch "$(printf '%s' "$out" | jq -r '.Credentials.Expiration')")"
  [ -n "$exp_epoch" ] || { warn "aws: could not parse expiry"; return 1; }

  # An ini file, not env vars: the aws CLI re-reads it on every invocation, so a
  # refresh needs no container restart.
  printf '%s' "$out" | jq -r '
    "[default]",
    "aws_access_key_id = \(.Credentials.AccessKeyId)",
    "aws_secret_access_key = \(.Credentials.SecretAccessKey)",
    "aws_session_token = \(.Credentials.SessionToken)"' \
    | dcx_write_secret "$creds/aws-credentials"

  printf '%s\n' "$exp_epoch" > "$creds/aws.expiry"
  info "minted aws session for $role (region ${region:-unset})"
}

# --- all ---------------------------------------------------------------------

dcx_mint_all() { # <instance>
  local name="$1" profile rc=0
  profile="$(dcx_meta "$name" '.profile')"
  dcx_profile_has_k8s "$profile" && { dcx_mint_k8s "$name" || rc=1; }
  dcx_profile_has_gcp "$profile" && { dcx_mint_gcp "$name" || rc=1; }
  dcx_profile_has_aws "$profile" && { dcx_mint_aws "$name" || rc=1; }
  return $rc
}
