# 1. Go with sqlc, not Python with an ORM

Status: accepted · 2026-10

## Context

The registry is a control plane: a small HTTP API, a background evaluator, an ingest
endpoint that pipelines call on every run, and a lot of SQL whose shape matters
(row locks on change requests, a partial unique index, a recursive lineage walk). Most of
the rest of this portfolio is Python.

## Decision

* Go 1.27 with `net/http` and chi. One static binary serves the API, runs the freshness
  evaluator and applies migrations.
* SQL is written as SQL in `api/queries/` and compiled to typed Go by sqlc. The generated
  code is committed; CI runs `sqlc diff` and fails if it is stale.
* Migrations are goose files embedded in the binary.

## Consequences

* The queries a reviewer reads are the queries that run. The lineage CTE, the
  `FOR UPDATE` on a change request and the one-open-request index are visible in one
  place, not reconstructed from ORM calls.
* sqlc needs help with nullability: aggregates and outer joins are wrapped in `COALESCE`
  so the generated types are honest. Dynamic filters use `sqlc.narg` with `IS NULL OR`,
  which is fine at this size and would want separate queries if a filter ever needed its
  own index.
* Ingest is a hot path; Go keeps per-event overhead low (MEASUREMENTS.md has the
  figures) and the race detector runs over every test.
* A Python team would have to read Go. The API is described in OpenAPI 3.1, so clients
  do not.
