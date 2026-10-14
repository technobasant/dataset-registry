# Measurements

Every figure here comes from the JSON files in [`bench/results`](bench/results), recorded on
2026-10-14 with `make bench` and `make lighthouse`. Where a measurement was repeated, the
figure is the median of three runs and the per-run values are in the JSON.

## Environment

| | |
|---|---|
| Host | Apple M5, 10 cores, 24 GB unified memory, macOS 27.0.1 |
| Containers | Docker Desktop 29.8.2; Linux VM with 8 CPUs and 11.7 GiB; 13 containers from other projects were running on the same host, idle but not stopped |
| PostgreSQL | 18.1 (`postgres:18.1-alpine`), stock configuration (`shared_buffers` 128 MB), in its own container |
| Registry | Go 1.27.2, linux/arm64; pgx pool of 32 connections (8 for the freshness runs) |
| Client | `drbench`, in a container on the same compose network as PostgreSQL |
| UI | Next.js 16.4.0 standalone server, React 19.3.0; Lighthouse 13.5.0 on Playwright's Chromium build 1248 |

**How the API is measured.** `drbench` starts the registry's real HTTP handler on a loopback
listener inside its container and drives it over HTTP, against a database it creates for
that run (`CREATE DATABASE`, migrations, nothing else in it). Client and server therefore
share the container's CPUs, and PostgreSQL is one network hop away on the same host, so
there is no real network latency in any figure below. Absolute numbers are this host's;
the shapes — what grows with what — are the point.

## 1. OpenLineage ingest

`drbench ingest`: 20,000 RunEvents per run after 500 unmeasured warm-up events, each
validated against the OpenLineage 2-0-2 schema and stored in one transaction (job upsert,
three dataset lookups or inserts, three edge upserts, and for `COMPLETE` events a
last-write update). Events alternate `START` and `COMPLETE`, touch 2,000 distinct datasets
and 500 jobs, two inputs and one output each. Latency is per HTTP request as the client
saw it.

| Client concurrency | Events/s (median; range) | p50 | p95 | p99 | Errors |
|---:|---:|---:|---:|---:|---:|
| 1 | 1,221 (1,167–1,263) | 0.72 ms | 1.09 ms | 2.39 ms | 0 |
| 8 | 4,496 (3,383–5,157) | 1.36 ms | 2.97 ms | 8.10 ms | 0 |
| 32 | 8,782 (6,613–8,791) | 3.14 ms | 6.79 ms | 14.81 ms | 0 |

Throughput grows 7× from one client to 32 while p50 grows 4×: the database, not the
handler, is the limit, and the run-to-run spread at 8 and 32 clients (up to 25%) is the
host being shared. Every event was stored: datasets are locked in a fixed order, and a
deadlock or serialization failure would be retried up to twice.
A real Airflow or Spark deployment sends a handful of events per task run, so 1,000 events/s
from a single connection is far more than this registry would see.

## 2. Lineage queries by graph size and hop count

`drbench lineage`: a layered DAG of 100, 1,000 or 10,000 datasets in 10 layers; every
dataset after the first layer is written by its own job reading three datasets from the
layer before (fan-in 3, so average fan-out 3). The graph is built through the ingest API
(10,000 datasets: 3.9 s). Each cell is 100 queries from random roots — upstream from the
last layer, downstream from the first, so every hop exists — issued one at a time. Nodes
counts datasets and jobs returned.

| Datasets | Hops | Upstream p50 / p95 | Nodes | Downstream p50 / p95 | Nodes (median / max) |
|---:|---:|---:|---:|---:|---:|
| 100 | 1 | 0.32 / 0.44 ms | 5 | 0.35 / 0.45 ms | 7 / 11 |
| 100 | 3 | 0.41 / 0.52 ms | 28 | 0.51 / 1.82 ms | 39 / 51 |
| 100 | 5 | 0.56 / 0.89 ms | 64 | 0.67 / 1.01 ms | 81 / 91 |
| 1,000 | 1 | 0.30 / 0.33 ms | 5 | 0.33 / 0.37 ms | 7 / 15 |
| 1,000 | 3 | 0.48 / 0.71 ms | 49 | 0.54 / 0.95 ms | 63 / 161 |
| 1,000 | 5 | 1.37 / 5.45 ms | 251 | 1.53 / 2.40 ms | 353 / 541 |
| 10,000 | 1 | 0.45 / 0.78 ms | 5 | 0.34 / 0.39 ms | 7 / 21 |
| 10,000 | 2 | 0.38 / 0.41 ms | 17 | 0.51 / 0.98 ms | 23 / 81 |
| 10,000 | 3 | 0.50 / 0.68 ms | 53 | 0.54 / 0.99 ms | 65 / 243 |
| 10,000 | 4 | 0.82 / 1.19 ms | 157 | 1.12 / 2.19 ms | 215 / 819 |
| 10,000 | 5 | 1.73 / 2.11 ms | 440 | 2.34 / 5.34 ms | 581 / 1,817 |

Hops 2 and 4 for 100 and 1,000 datasets are in [`lineage.json`](bench/results/lineage.json).

- **Latency follows the size of the neighbourhood, not the size of the graph.** One hop
  costs the same at 100 and 10,000 datasets. At five hops the 10,000-dataset graph returns
  ~7× the nodes of the 100-dataset one (which the walk has nearly exhausted) and takes
  ~3–4× as long.
- **Downstream has the long tail.** Fan-in is fixed at 3, but fan-out varies, so a popular
  dataset's downstream at five hops returned 1,817 nodes; its p95 is 5.3 ms.
- **The one noisy cell** is 1,000 datasets, upstream, five hops: p50 was 1.2, 1.4 and
  2.7 ms and p95 1.7, 5.5 and 7.2 ms across the three runs. The same queries ran in each
  run, so I read it as contention on the shared host, but I did not prove that.
- The recursive CTE re-visits a node once per path and depth before `GROUP BY` collapses
  them. On a dense graph with many paths, deep queries would grow faster than the result;
  this DAG is not dense enough to show it. The API caps depth at 10.

## 3. Read API latency

`drbench reads`: 1,000 registered datasets (owner, SLO, a contract; 200 of them with two
consumers) and their lineage; 2,000 requests per endpoint per run from 8 concurrent clients.

| Endpoint | p50 | p95 | Requests/s |
|---|---:|---:|---:|
| `GET /api/v1/datasets?limit=1000` (all 1,000) | 12.9 ms | 22.1 ms | 573 |
| `GET /api/v1/datasets/{id}` (consumers, versions, change request, SLO history) | 0.51 ms | 0.85 ms | 14,701 |
| `GET /api/v1/datasets/{id}/lineage?depth=2&direction=both` | 1.47 ms | 2.72 ms | 4,723 |

The list is the weak spot: three correlated subqueries per row (consumer count, current
version, open change request) and no pagination beyond a limit. 13 ms for 1,000 rows is
fine for the UI; a catalogue of 100,000 datasets would need keyset pagination and the
counts kept on the row.

## 4. Freshness breach detection delay

`drbench freshness`: 200 datasets with a 60 s SLO whose deadlines fall evenly over two
evaluator intervals, the evaluator started at a random phase. The delay is
`detected_at − deadline` for each breach, as the registry recorded it.

| Evaluator interval | Delay p50 | p95 | Max | Uniform-arrival expectation (p50 / p95 / max) |
|---:|---:|---:|---:|---|
| 1 s | 0.50 s | 0.95 s | 1.00 s | 0.5 / 0.95 / 1 s |
| 5 s | 2.49 s | 4.71 s | 4.96 s | 2.5 / 4.75 / 5 s |
| 15 s | 7.50 s | 14.19 s | 14.93 s | 7.5 / 14.25 / 15 s |

The delay is exactly what an interval scheduler predicts: half an interval at the median,
up to one interval in the worst case. One of the three 15 s runs had a maximum of 15.30 s —
0.3 s beyond the interval — which is the evaluation itself plus the ticker running late on
a busy host. The default interval (15 s) therefore adds up to 15 s on top of whatever SLO
an owner sets; for SLOs measured in hours that is noise, for a 10-minute Kafka topic it is
2.5%.

**What an evaluation costs.** With 10,000 datasets under an SLO, a steady-state pass (nine
status changes) takes **30 ms**. A pass in which all 10,000 change status (the first one after a
restart with stale statuses, or a platform-wide outage) takes **3.1 s** (2.7–3.2 s): each
transition is its own small transaction. That is the first thing to batch if the registry
grows.

## 5. Compatibility checker accuracy

`drbench compat`: the 51 hand-labelled changes in
[`api/internal/compat/cases.json`](api/internal/compat/cases.json), each labelled for
BACKWARD and FORWARD separately. They cover additions and removals of optional and required
fields in open and closed objects, optional → required and back, integer → number widening
and number → integer narrowing, nullability, enum values added, removed, introduced and
dropped, length and numeric bounds, `additionalProperties`, nested objects, arrays, renames,
`format`, `pattern`, `const`, `anyOf`, `multipleOf` and annotation-only changes.

**Are the labels right?** An oracle generates probe documents from both schemas (exemplars,
then every field set to values of every type, enum values, examples and bound edges, fields
dropped and added) and validates each against both versions with a real JSON Schema
validator. A document valid under one and invalid under the other proves breakage in that
direction. Across 8,092 probes the oracle agrees with **51 of 51** labels in both
directions.

| Mode | Breaking, flagged | Safe, flagged (false positive) | Safe, passed | Breaking, passed (false negative) | Accuracy |
|---|---:|---:|---:|---:|---:|
| BACKWARD | 29 | 2 | 20 | **0** | 96.1% |
| FORWARD | 27 | 2 | 22 | **0** | 96.1% |
| FULL | 47 | 0 | 4 | **0** | 100% |

- **No breaking change passed as safe** in any mode. That is the property the change-request
  workflow depends on, and a unit test enforces it on every case.
- **The four false positives are the ones designed in:** a widened and a narrowed regular
  expression (containment is not computed), an `anyOf` branch added, and `multipleOf` added.
  Each is flagged in both directions; each truly breaks only one.
- **FULL's 100% is not a better checker.** 47 of the 51 changes break *some* direction, so
  under FULL the conservative answer is right for all four cases it over-reports elsewhere.
- **The labels are strict**, and two of them surprise people: adding an optional field to
  an open object breaks BACKWARD, and removing an optional field from one breaks FORWARD.
  Both are proved by the oracle with a concrete document.
- The labelled set was written alongside the checker by the same person. It tests the rules
  the checker claims to implement; it is not an independent sample of real-world schema
  changes, and a real corpus would contain far more `$ref` and `oneOf`, which this checker
  will always call breaking.

A comparison takes 17 µs at the median (64 µs p95).

## 6. Lighthouse

`make lighthouse` on the seeded stack: three pages, desktop and mobile presets, three runs
each, median shown. The mobile preset is Lighthouse's default: an emulated mid-range phone
on simulated slow 4G.

| Page | Preset | Performance | Accessibility | Best practices | SEO | LCP |
|---|---|---:|---:|---:|---:|---:|
| Dataset list | desktop | 100 | 100 | 100 | 100 | 0.43 s |
| Dataset page (lineage graph, diff) | desktop | 100 | 100 | 100 | 100 | 0.47 s |
| Change request | desktop | 100 | 100 | 100 | 100 | 0.43 s |
| Dataset list | mobile | 100 | 100 | 100 | 100 | 1.88 s |
| Dataset page (lineage graph, diff) | mobile | 99 | 100 | 100 | 100 | 2.18 s |
| Change request | mobile | 100 | 100 | 100 | 100 | 1.88 s |

Server-rendered pages with no UI kit leave little to optimise; the dataset page pays for
React Flow on mobile. An accessibility score of 100 means the automated audits found
nothing. It does not mean the lineage graph is usable without a mouse — the list beside it
is the accessible version — and no screen-reader testing was done.

## 7. Tests

`go test -race` over the Go module: unit tests for the checker (the labelled cases), the
state machine, tokens and the freshness classifier; integration tests that run the HTTP API
against PostgreSQL 18 — ownership before consumption, the full change-request lifecycle,
rejection, unregistering, authorization, schema validation, lineage ingest and queries, the
evaluator, the audit log's triggers. Statement coverage of `api/internal/...` (generated
query code included): **73.6%**. Playwright drives the same lifecycle and an SLO breach
through the UI.

## Reproducing

```bash
make up                # PostgreSQL, API, UI, Prometheus
make bench             # writes bench/results/{compat,ingest,lineage,reads,freshness}.json
make seed lighthouse   # writes bench/results/lighthouse.json
```

Each benchmark creates its own database (`bench_*`), so the demo data is untouched.
