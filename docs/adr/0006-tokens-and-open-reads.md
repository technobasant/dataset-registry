# 6. Signed role tokens, open reads, and a development issuer

Status: accepted · 2026-10

## Context

The interesting rules are authorization rules: an owner registers datasets only for their
team, a consumer answers only for theirs, an admin cannot acknowledge on a team's behalf.
They need a caller with a role and a team to be testable. Building an identity provider
is not the point.

## Decision

* HS256 bearer tokens carrying `sub`, `role` (`admin`, `owner`, `consumer`) and `team`,
  with an issuer and an expiry. One shared key.
* Reads are open. A catalogue is for finding things; hiding which datasets exist from the
  people who might depend on them defeats it.
* Writes need a token. Lineage ingest takes an owner or admin token: pipelines write as
  the team that runs them.
* `POST /api/v1/dev/tokens` mints any token and exists only when `DR_DEV_TOKENS=true`. The
  web UI uses it behind a persona switcher; the API logs a warning at start-up when it is
  on.

## Consequences

* This is not production authentication, and the README says so. A deployment would
  verify tokens from its identity provider (OIDC) and map groups to roles in one place,
  `internal/auth`.
* A shared HMAC key means anyone who can mint tokens for pipelines can mint them for
  people.
* Open reads expose dataset names, owners and lineage to anyone who can reach the API.
