#!/bin/sh
# Generates a signed RS256 JWT for local development.
# Usage: ./scripts/generate-jwt.sh <user-id> [role]
# Requires: openssl, python3 or node

set -e

USER_ID="${1:-00000000-0000-0000-0000-000000000001}"
ROLE="${2:-user}"
PRIVATE_KEY="${JWT_PRIVATE_KEY_PATH:-keys/private.pem}"

if [ ! -f "$PRIVATE_KEY" ]; then
  echo "Generating RSA key pair in keys/ ..."
  mkdir -p keys
  openssl genpkey -algorithm RSA -out keys/private.pem -pkeyopt rsa_keygen_bits:2048 2>/dev/null
  openssl rsa -pubout -in keys/private.pem -out keys/public.pem 2>/dev/null
  echo "Keys written to keys/private.pem and keys/public.pem"
fi

HEADER=$(echo -n '{"alg":"RS256","typ":"JWT"}' | openssl base64 -A | tr '+/' '-_' | tr -d '=')
NOW=$(date +%s)
EXP=$((NOW + 86400))
PAYLOAD=$(printf '{"sub":"%s","role":"%s","iat":%d,"exp":%d}' "$USER_ID" "$ROLE" "$NOW" "$EXP" \
  | openssl base64 -A | tr '+/' '-_' | tr -d '=')

SIG=$(printf '%s.%s' "$HEADER" "$PAYLOAD" \
  | openssl dgst -sha256 -sign "$PRIVATE_KEY" \
  | openssl base64 -A | tr '+/' '-_' | tr -d '=')

JWT="$HEADER.$PAYLOAD.$SIG"
printf '%s\n' "$JWT"       # stdout — captured by TOKEN=$(...)
printf '%s\n' "$JWT" >&2  # stderr — always visible in terminal
