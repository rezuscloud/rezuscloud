# How to manage users and API tokens

> **Type:** How-to · **Audience:** Admin

## Overview

Create users, assign roles, and generate API tokens for programmatic access.

## Roles

| Role | Permissions |
|------|-------------|
| `admin` | Everything (create/delete users, tenants, all operations) |
| `edit` | Create/update tenants, node groups, machines (no user management) |
| `view` | Read-only access |

## Steps

1. **Create a user:**
   ```bash
   curl -X POST $SERVER/api/v1/users \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"metadata":{"name":"alice"},"spec":{"role":"edit","password":"secret123"}}'
   ```

2. **Create an API token** for programmatic access:
   ```bash
   curl -X POST $SERVER/api/v1/users/alice/tokens \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"metadata":{"name":"ci-token"},"spec":{"expiresIn":"8760h"}}'
   ```

3. **Use the token:**
   ```bash
   export REZUSCLOUD_TOKEN="<token-value>"
   rezusctl get clusters --server $SERVER
   ```

## See also

- [Auth ADR](../adr/0012-auth-local-jwt-and-api-tokens.md)
