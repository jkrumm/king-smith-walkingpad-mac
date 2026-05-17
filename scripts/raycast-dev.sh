#!/usr/bin/env bash
# Bring up the Raycast dev loop with the Node version pinned in
# raycast/.nvmrc. Detects whichever version manager the operator already has
# (fnm, mise, asdf, volta, nvm) and uses it to install + run with the right
# Node — no manual `nvm use` step required. Falls back to the current Node
# if it's already on a 22.x or newer, and prints a friendly nudge if no
# manager is installed at all.
#
# Called from `make up`; safe to run standalone.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
RAYCAST_DIR="$SCRIPT_DIR/../raycast"
NODE_VERSION=$(tr -d '[:space:]' < "$RAYCAST_DIR/.nvmrc")

RAYCAST_CMD='npm install && npm run dev'

run_with_current() {
  echo "→ using $(node -v) (already on a supported Node)"
  exec bash -c "cd '$RAYCAST_DIR' && $RAYCAST_CMD"
}

run_with() {
  local tool=$1
  shift
  echo "→ using $tool to activate Node $NODE_VERSION"
  exec "$@"
}

# Best-case path: current Node is already in the supported range. The npm
# `engines` field requires >=22.22.2; anything on the v22 line works for our
# needs.
if command -v node >/dev/null 2>&1; then
  current=$(node -v 2>/dev/null || echo "")
  if [[ "$current" =~ ^v(2[2-9]|[3-9][0-9])\. ]]; then
    run_with_current
  fi
fi

# fnm — what jkrumm uses.
if command -v fnm >/dev/null 2>&1; then
  fnm install "$NODE_VERSION" >/dev/null 2>&1 || fnm install "$NODE_VERSION"
  run_with fnm fnm exec --using="$NODE_VERSION" -- bash -c "cd '$RAYCAST_DIR' && $RAYCAST_CMD"
fi

# mise (formerly rtx).
if command -v mise >/dev/null 2>&1; then
  mise install "node@$NODE_VERSION" >/dev/null 2>&1 || mise install "node@$NODE_VERSION"
  run_with mise mise exec "node@$NODE_VERSION" -- bash -c "cd '$RAYCAST_DIR' && $RAYCAST_CMD"
fi

# asdf — needs the nodejs plugin; install only if missing.
if command -v asdf >/dev/null 2>&1; then
  if ! asdf plugin list 2>/dev/null | grep -q '^nodejs$'; then
    asdf plugin add nodejs >/dev/null 2>&1 || true
  fi
  asdf install nodejs "$NODE_VERSION" >/dev/null 2>&1 || asdf install nodejs "$NODE_VERSION" || true
  run_with asdf env ASDF_NODEJS_VERSION="$NODE_VERSION" bash -c "cd '$RAYCAST_DIR' && $RAYCAST_CMD"
fi

# volta — shims handle version selection at runtime.
if command -v volta >/dev/null 2>&1; then
  volta install "node@$NODE_VERSION" >/dev/null 2>&1 || true
  run_with volta volta run --node "$NODE_VERSION" bash -c "cd '$RAYCAST_DIR' && $RAYCAST_CMD"
fi

# nvm — shell function, so source the loader script before invoking.
NVM_DIR=${NVM_DIR:-$HOME/.nvm}
for nvm_path in \
  "$NVM_DIR/nvm.sh" \
  "/opt/homebrew/opt/nvm/nvm.sh" \
  "/usr/local/opt/nvm/nvm.sh"; do
  if [ -s "$nvm_path" ]; then
    echo "→ using nvm to activate Node $NODE_VERSION"
    exec bash -c '
      set -e
      . "'"$nvm_path"'"
      nvm install "'"$NODE_VERSION"'" >/dev/null
      nvm use "'"$NODE_VERSION"'" >/dev/null
      cd "'"$RAYCAST_DIR"'" && '"$RAYCAST_CMD"
  fi
done

cat <<EOF
no Node version manager detected (tried fnm, mise, asdf, volta, nvm).
either:
  - install one of them (recommended: fnm — \`brew install fnm\`),
  - or upgrade Node to $NODE_VERSION manually,
then re-run \`make up\`.

daemon is already deployed and live — only the Raycast dev loop is gated.
EOF
exit 0
