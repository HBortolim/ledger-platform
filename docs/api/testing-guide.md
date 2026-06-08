# Local Testing Guide

How to generate JWT tokens and test the wallet-service endpoints from the command line.

## Prerequisites

- `openssl` — for key pair generation and token signing
- `curl` — for HTTP requests
- Java 21+ and Maven — to run the service
- The service running on `localhost:8080` (see [Starting the service](#starting-the-service))

---

## 1. Generate the RSA key pair and a JWT

All commands run from the **repo root** (`ledger-platform/`).

### Generate a user token

```sh
TOKEN=$(./scripts/generate-jwt.sh 10000000-0000-0000-0000-000000000001)
```

On first run the script creates `keys/private.pem` and `keys/public.pem` automatically.
Subsequent runs reuse the existing key pair.

The token encodes:

| Claim | Value |
|-------|-------|
| `sub` | the user ID you passed (must be a UUID) |
| `role` | `user` (default) |
| `iat` | current Unix timestamp |
| `exp` | `iat + 86400` (valid for 24 hours) |

### Generate an admin token

```sh
ADMIN_TOKEN=$(./scripts/generate-jwt.sh 10000000-0000-0000-0000-000000000001 admin)
```

Pass any user UUID as the first argument and `admin` as the second. The `role: admin`
claim is required for deposit and withdrawal endpoints (not yet implemented).

### Inspect a token

To decode the payload without a dedicated tool:

```sh
echo $TOKEN | cut -d. -f2 | python3 -c "import sys,base64; print(base64.urlsafe_b64decode(sys.stdin.read().strip()+'==').decode())" | python3 -m json.tool
```

Example output:

```json
{
    "sub": "10000000-0000-0000-0000-000000000001",
    "role": "user",
    "iat": 1748563200,
    "exp": 1748649600
}
```

---

## 2. Starting the service

Run from `services/wallet-service/`. The service needs to find the public key — the
path defaults to `keys/public.pem` relative to the working directory, so point it at
the key the script generated:

```sh
cd services/wallet-service
JWT_PUBLIC_KEY_PATH=../../keys/public.pem mvn spring-boot:run
```

The service is ready when you see `Started WalletServiceApplication` in the logs.

---

## 3. Testing the endpoints

### Public paths — no token required

```sh
curl -s http://localhost:8080/health/live
# {"status":"UP"}

curl -s http://localhost:8080/health/ready
# {"status":"UP"}
```

---

### Authentication checks

**No token → 401:**
```sh
curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST http://localhost:8080/wallets \
  -H "Content-Type: application/json" \
  -d '{"ownerId":"10000000-0000-0000-0000-000000000001","currency":"BRL"}'
# 401
```

**Tampered token → 401:**
```sh
curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer ${TOKEN}tampered"
# 401
```

**Wrong user in token trying to create a wallet for another user → 403:**
```sh
OTHER_TOKEN=$(./scripts/generate-jwt.sh 20000000-0000-0000-0000-000000000002)

curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $OTHER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"ownerId":"10000000-0000-0000-0000-000000000001","currency":"BRL"}'
# 403 — token sub doesn't match ownerId
```

---

### `POST /wallets` — Create wallet

The only implemented endpoint. The `ownerId` in the request body **must match** the
`sub` claim in the JWT.

```sh
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "ownerId": "10000000-0000-0000-0000-000000000001",
    "currency": "BRL"
  }' | python3 -m json.tool
```

Expected `201 Created`:

```json
{
    "walletId": "019703a1-...",
    "ownerId": "10000000-0000-0000-0000-000000000001",
    "currency": "BRL",
    "status": "ACTIVE",
    "createdAt": "2026-05-30T10:00:00.000Z"
}
```

**Validation errors:**

```sh
# Unsupported currency → 422
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"ownerId":"10000000-0000-0000-0000-000000000001","currency":"USD"}'
```

---

### Endpoints not yet implemented

The following endpoints return `501 Not Implemented`. Authentication is enforced —
a missing or invalid token still returns `401` before the stub is reached.

| Method | Path | Status |
|--------|------|--------|
| `GET`  | `/wallets/{walletId}/balance` | stub |
| `POST` | `/wallets/{walletId}/freeze` | stub |
| `POST` | `/wallets/{walletId}/unfreeze` | stub |
| `POST` | `/wallets/{walletId}/close` | stub |
| `POST` | `/transfers` | stub |

Example — confirm auth is enforced on a stub:

```sh
# No token → 401 (auth checked before stub)
curl -s -o /dev/null -w "%{http_code}\n" \
  http://localhost:8080/wallets/some-id/balance
# 401

# Valid token → 501 (auth passed, stub hit)
curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/wallets/some-id/balance
# 501
```

---

## 4. Key pair lifecycle

| File | Tracked in git | Purpose |
|------|---------------|---------|
| `keys/private.pem` | No (`keys/.gitignore`) | Signs tokens; only used by the script |
| `keys/public.pem` | Yes (safe to commit) | Verifies tokens; loaded by the service at startup |

To rotate the key pair, delete both files and re-run `./scripts/generate-jwt.sh`.
All previously issued tokens will be invalid (they were signed with the old private key).