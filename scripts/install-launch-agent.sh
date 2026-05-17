#!/usr/bin/env bash
# Render the LaunchAgent plist with HOME substituted and (optionally) the Argo
# token baked into EnvironmentVariables. The Argo token comes from 1Password
# (op://common/api/SECRET) which is the same secret argo itself reads.
#
# Without `op` available or with the 1Password lookup failing, the plist gets
# rendered without any Argo env vars — the daemon will start, see no token,
# log "sync.disabled", and run fine without uploading sessions. Argo sync is
# strictly optional (PRD §1).
#
# Usage:  scripts/install-launch-agent.sh <template-path> <output-path>

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <template> <output>" >&2
  exit 64
fi

TEMPLATE=$1
OUTPUT=$2

if [[ ! -f "$TEMPLATE" ]]; then
  echo "error: template not found: $TEMPLATE" >&2
  exit 1
fi

# Resolve the Argo token from 1Password. Quiet on failure — we just skip the
# env vars and let the daemon run without sync.
ARGO_TOKEN=""
ARGO_URL="https://argo.jkrumm.com/api"
if command -v op >/dev/null 2>&1; then
  if token=$(op read "op://common/api/SECRET" --account tkrumm 2>/dev/null) && [[ -n "$token" ]]; then
    ARGO_TOKEN=$token
    echo "argo sync: token resolved from 1Password (op://common/api/SECRET)"
  else
    echo "argo sync: 1Password lookup failed — installing without token (daemon will skip sync)"
  fi
else
  echo "argo sync: \`op\` CLI not found — installing without token (daemon will skip sync)"
fi

# Build the EnvironmentVariables block. Keep indentation aligned with the
# template so the rendered plist is readable.
if [[ -n "$ARGO_TOKEN" ]]; then
  ARGO_ENV=$(cat <<EOF
    <key>KSWP_ARGO_URL</key>    <string>${ARGO_URL}</string>
    <key>KSWP_ARGO_TOKEN</key>  <string>${ARGO_TOKEN}</string>
EOF
)
else
  ARGO_ENV=""
fi

# Write to a temp file first so a sed failure doesn't truncate $OUTPUT.
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

# Substitute HOME and the Argo env block. Using awk for __ARGO_ENV__ so we can
# inject a multiline payload without sed-escape gymnastics.
ARGO_ENV="$ARGO_ENV" awk -v home="$HOME" '
  {
    gsub(/__HOME__/, home)
    if ($0 ~ /__ARGO_ENV__/) {
      env = ENVIRON["ARGO_ENV"]
      if (env != "") print env
      next
    }
    print
  }
' "$TEMPLATE" > "$TMP"

# Tighten perms — the file may contain a bearer token.
install -m 0600 "$TMP" "$OUTPUT"
echo "wrote $OUTPUT"
