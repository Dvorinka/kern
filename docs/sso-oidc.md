# SSO / OIDC scoping

Status: **scoped, not implemented.** Local argon2id accounts remain the only
auth mechanism (see ADR-002). This document fixes the design so implementation
starts against a concrete IdP choice rather than in the abstract.

## Why it is the enterprise gate

Every comparable self-hosted CI (Woodpecker, Forgejo Actions, Concourse with
an auth plugin) treats OIDC as table stakes: orgs already run Dex, Keycloak,
or Authentik and will not create a parallel password database for KERN.
Everything else in `docs/production.md` is smaller than this.

## Provider choice (decide first)

Implement against **generic OIDC** — the protocol is identical across Dex,
Keycloak, and Authentik; only discovery URL and claim shapes differ.

| Provider | Notes |
| --- | --- |
| Dex | Lightest; GitHub/GitLab/LDAP connectors built in. Good default for small teams. |
| Keycloak | Heaviest; full IdP. Pick when the org already runs it. |
| Authentik | Middle ground; good if already deployed. |

Configuration surface (env, mirroring existing `KERN_*` style):

```
KERN_OIDC_ISSUER=https://idp.example.com/realms/kern
KERN_OIDC_CLIENT_ID=kern
KERN_OIDC_CLIENT_SECRET=...
KERN_OIDC_REDIRECT_URL=https://ci.example.com/api/v1/auth/oidc/callback
KERN_OIDC_SCOPES=openid profile email groups
KERN_OIDC_GROUPS_CLAIM=groups        # or roles/groups depending on IdP
KERN_OIDC_DEFAULT_ROLE=member        # role for users with no mapped group
KERN_OIDC_ADMIN_GROUP=kern-admins    # group -> owner|admin mapping below
KERN_OIDC_OWNER_GROUP=kern-owners
```

All empty = feature off. Local login stays enabled as break-glass regardless.

## Flows

1. `GET /auth/oidc/login` → 302 to `{issuer}/auth` with PKCE (S256), `state`,
   and `nonce` stored server-side (short-lived signed cookie or a
   `oidc_flows` table row — table preferred: survives restarts and works
   across replicas without sticky sessions).
2. `GET /auth/oidc/callback` → verify `state`/`nonce`, exchange code at the
   token endpoint, validate the ID token (issuer, audience, expiry, signature
   via JWKS with kid-cache and `alg` allowlist — never `none`), then:
   - Resolve user by `email` claim (case-insensitive, same as password
     login). No match → JIT-provision a `users` row with `password_hash`
     empty and a marker (`auth_source=oidc`) — gated by
     `KERN_OIDC_JIT=true`; without it, unknown users get a clear error, not
     a silent account.
   - Role resolution: intersect token `groups` claim with configured
     group→role map (`KERN_OIDC_GROUP_ROLES=admins:admin,owners:owner` style
     or per-group env pairs — pick one format at implementation). Highest
     mapped role wins; unmapped → `KERN_OIDC_DEFAULT_ROLE`. Roles write to
     `org_members.role` only if the mapped role **exceeds** nothing the user
     already holds manually? — no: IdP is authoritative for mapped users,
     local role is authoritative for break-glass accounts. Concretely:
     JIT users get IdP role on every login; existing local accounts keep
     their role unless they also hold an IdP identity link.
3. Session issuance: identical to password login — `CreateSession` +
   `kern_session` cookie. Sessions remain revocable via the new
   `/auth/sessions` endpoints; logout clears the cookie only (IdP session
   persists, standard OIDC behavior — document, do not try to end it).

## Identity linking

Add `auth_identities` (or a column pair on `users`):

- `user_id`, `provider` ('oidc'), `subject` (the `sub` claim — stable,
  pairwise per client), `email`, `created_at`.
- Unique on `(provider, subject)`.
- First OIDC login for an email that matches a local account **links** the
  identity only if the IdP asserts `email_verified=true`; otherwise require
  an admin to link manually. This is the account-takeover guard — an IdP
  that lets users set arbitrary emails must not be able to claim a local
  admin's account.
- Break-glass: local accounts with passwords keep working. An operator can
  also mark an account `oidc_only` later; out of scope for v1.

## Group → role mapping

Existing roles: `owner`, `admin`, `member` (`org_members.role`; enforcement
already shipped via `requireRole`). Mapping is deliberately coarse — KERN
has three roles, IdPs have dozens of groups. Ship:

```
KERN_OIDC_ROLE_CLAIM=groups            # claim carrying group names
KERN_OIDC_ROLE_MAP=group:role,group:role   # e.g. ci-admins:admin,ci-owners:owner
```

Precedence: `owner` > `admin` > `member`. No match → default role. A user
removed from all mapped groups drops to `KERN_OIDC_DEFAULT_ROLE` on next
login — document that de-provisioning is login-time, not instant; instant
revoke stays possible via session deletion (already shipped).

## What this does NOT include (v1)

- SAML — different protocol, different market; revisit only on demand.
- SCIM provisioning — group sync at login covers the 90% case.
- Per-project IdP policies — org-level only.
- IdP-initiated SLO / back-channel logout — cookie logout is enough for v1.
- Multiple IdPs per org — one issuer per instance.

## Implementation checklist (when an IdP is chosen)

1. `internal/oidc` package: discovery fetch + JWKS cache (use `coreos/go-oidc`
   or `github.com/zitadel/oidc` — evaluate then; stdlib JWT verify is ~150
   lines but JWKS rotation handling is where libraries earn their keep).
2. `oidc_flows` migration + store methods.
3. Routes: `/auth/oidc/login`, `/auth/oidc/callback`; settings card "SSO"
   showing connection status.
4. `auth_identities` migration; link-on-first-login with
   `email_verified` guard.
5. Login page: "Sign in with SSO" button when configured.
6. Tests: state/nonce rejection, JWKS kid rotation, unverified-email link
   refusal, role precedence, JIT on/off.
7. Docs: `docs/deploy.md` IdP setup per provider (Dex example is cheapest).

Estimated size: ~600-900 lines Go + ~150 lines UI + one migration. The
session/audit/member plumbing it plugs into is already built.
