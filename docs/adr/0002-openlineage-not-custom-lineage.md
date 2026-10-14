# 2. Lineage arrives as OpenLineage run events

Status: accepted · 2026-10

## Context

Lineage has to come from the systems that move data: Airflow, dbt, Spark, Flink. A
registry with its own lineage format needs an adapter per system and goes stale the day
someone adds a pipeline without one.

## Decision

* `POST /api/v1/lineage` accepts one OpenLineage `RunEvent`, the body the OpenLineage
  HTTP transport sends. It is validated against the OpenLineage 2-0-2 JSON Schema, shipped
  with the API; an invalid event is a 400 with the validator's output.
* Every event with inputs or outputs records edges `dataset → job → dataset`, with first
  and last seen times. A `COMPLETE` event is a successful write to each output; the
  dataset's last write only moves forward, so a late event for an older run cannot make a
  dataset look staler than it is.
* A dataset first seen in lineage is created **unregistered**. It is real, something
  writes it, and nobody owns it. Nobody can register as its consumer until a team claims
  it.

## Consequences

* Any producer with an OpenLineage integration works without code here. The seed data
  uses the producer URIs that the Airflow, dbt and Spark integrations send.
* Lineage is the union of every edge ever observed. An edge that stopped existing stays
  until someone deletes the job; `last_seen` is there so a UI could fade old edges, which
  this one does not yet do.
* The registry trusts the event's `eventTime`. A producer with a wrong clock moves
  freshness with it.
* Facets (schema, data quality, column lineage) are accepted and ignored. Comparing an
  output's schema facet with the dataset's contract is on the roadmap.
