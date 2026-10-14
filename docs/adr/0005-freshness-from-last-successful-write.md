# 5. Freshness is measured from the last successful write, on an interval

Status: accepted · 2026-10

## Context

An owner's freshness promise is "this dataset is never more than N minutes old". The
registry needs to know when that stops being true, without querying the data itself.

## Decision

* Staleness is the time since the last `COMPLETE` OpenLineage event that named the
  dataset as an output. A dataset with an SLO that has never been written is measured
  from its registration.
* `healthy` up to 75% of the SLO, `at_risk` above that, `breached` above the SLO.
* An evaluator checks every dataset with an SLO on a fixed interval (15 s by default,
  10 s in the demo), records each status change with the deadline that was missed, and
  exports staleness, status and detection delay to Prometheus. Alerting is Prometheus's
  job; the demo ships two rules.

## Consequences

* Detection lags the breach by up to one interval plus one evaluation. MEASUREMENTS.md
  measures the lag at three intervals instead of assuming it.
* A job that reports `COMPLETE` after writing nothing keeps its dataset fresh. The
  registry measures that pipelines ran, not that data arrived; row counts from an output
  statistics facet would close part of that gap.
* One query reads every SLO each tick. Evaluating 10,000 datasets is measured; past that
  the evaluator would want to look only at datasets whose deadline falls before the next
  tick.
* Clock skew between producers and the registry shows up as freshness error.
