# Changelog

## 0.1.0 — 2026-10-14

First release.

- Go API on PostgreSQL 18 (chi, pgx, sqlc, goose): datasets with an owning team and
  person, tier, freshness SLO and compatibility mode; datasets discovered through lineage
  are created unowned and cannot be depended on until a team claims them.
- Versioned JSON Schema contracts with a compatibility checker that reports each change as
  breaking BACKWARD, FORWARD or both, with a reason; FORWARD by default. 51 labelled
  changes, cross-checked by a probe oracle running a real validator.
- Change requests: a breaking change waits for every consumer registered when it opened;
  open → approved → published, or rejected / withdrawn; one open request per dataset;
  append-only audit log enforced by triggers.
- OpenLineage ingest validated against the 2-0-2 RunEvent schema; dataset–job lineage with
  N-hop upstream and downstream queries; last successful write per dataset.
- Freshness evaluator (healthy, at risk, breached) with transition history and Prometheus
  metrics, plus two alert rules.
- HS256 role tokens (admin, owner, consumer) and a development token issuer — not
  production authentication.
- Next.js UI: datasets by SLO status, dataset pages with consumers, contract diffs and the
  checker's report, a React Flow lineage graph, change-request review and publish.
- `make up`, `make seed`, `make demo` for Lumen & Loom, an online homewares retailer; Playwright
  end-to-end tests; measurements of ingest, lineage queries, reads, breach detection, the
  checker and Lighthouse in MEASUREMENTS.md.
