#!/usr/bin/env bash
# Build dist/autounattend.iso for the Kerberos lab DC. Substitutes
# @@VAR@@ placeholders in the .tpl files from environment variables,
# then packages the staged files into a small ISO via xorriso.
#
# Invoked by `task lab:build-iso`; can also be run directly. See
# examples/lab/kerberos/README.md for the surrounding context.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SRC_DIR/../../.." && pwd)"
OUT="$ROOT/dist/autounattend.iso"
STAGE="$ROOT/dist/lab-iso-stage"

: "${HVLAB_ADMIN_PASSWORD:?set HVLAB_ADMIN_PASSWORD in .env.local (lab Administrator password)}"
: "${HVLAB_DSRM_PASSWORD:?set HVLAB_DSRM_PASSWORD in .env.local (DSRM recovery password)}"

if ! command -v xorriso >/dev/null 2>&1; then
    echo 'xorriso not found on PATH. Install with: brew install xorriso  (or: apt install xorriso)' >&2
    exit 1
fi

# Python substitution rather than sed so passwords with regex-meta
# or shell-meta chars (|, /, \, &, $) don't break the build.
substitute() {
    python3 - "$1" "$2" "$3" "$4" <<'PY'
import os, sys
src, dst, placeholder, env_var = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
with open(src) as f:
    body = f.read()
with open(dst, 'w') as f:
    f.write(body.replace(f'@@{placeholder}@@', os.environ[env_var]))
PY
}

mkdir -p "$STAGE" "$(dirname "$OUT")"
# Stage dir contains rendered passwords. Wipe on every exit path
# (success, xorriso failure, substitute failure, ^C) so a botched
# build never leaves cleartext on disk.
trap 'rm -rf "$STAGE"' EXIT

substitute "$SRC_DIR/autounattend.xml.tpl" "$STAGE/autounattend.xml" \
           ADMIN_PASSWORD HVLAB_ADMIN_PASSWORD
substitute "$SRC_DIR/FirstLogon.ps1.tpl"   "$STAGE/FirstLogon.ps1"   \
           DSRM_PASSWORD  HVLAB_DSRM_PASSWORD

# -V: volume label (the FirstLogon.ps1 search loop in autounattend.xml
#     doesn't actually use the label, but a known label is helpful for
#     manual mount/inspect).
# -J -r: Joliet + Rock Ridge so Windows installer reads filenames with
#        case preserved.
xorriso -as mkisofs -quiet -V HVLABUNATT -J -r -o "$OUT" "$STAGE"

echo "built $OUT"
