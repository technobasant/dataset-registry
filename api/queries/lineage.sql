-- name: EnsureLineageDataset :one
-- Datasets first seen in lineage are created unregistered: nobody owns them yet.
INSERT INTO datasets (namespace, name, registered)
VALUES ($1, $2, FALSE)
ON CONFLICT (namespace, name) DO UPDATE SET namespace = EXCLUDED.namespace
RETURNING id;

-- name: UpsertJob :one
INSERT INTO jobs (namespace, name, last_run_id, last_state, last_event_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (namespace, name) DO UPDATE
SET last_run_id   = CASE WHEN jobs.last_event_at IS NULL OR jobs.last_event_at <= EXCLUDED.last_event_at
                         THEN EXCLUDED.last_run_id ELSE jobs.last_run_id END,
    last_state    = CASE WHEN jobs.last_event_at IS NULL OR jobs.last_event_at <= EXCLUDED.last_event_at
                         THEN EXCLUDED.last_state ELSE jobs.last_state END,
    last_event_at = GREATEST(jobs.last_event_at, EXCLUDED.last_event_at)
RETURNING id;

-- name: UpsertEdge :exec
INSERT INTO lineage_edges (job_id, dataset_id, direction, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (job_id, dataset_id, direction) DO UPDATE
SET first_seen = LEAST(lineage_edges.first_seen, EXCLUDED.first_seen),
    last_seen  = GREATEST(lineage_edges.last_seen, EXCLUDED.last_seen);

-- name: RecordWrite :execrows
-- Only moves forward: a late COMPLETE for an older run does not make a dataset look staler.
UPDATE datasets
SET last_write_at = $2, last_write_run = $3
WHERE id = $1 AND (last_write_at IS NULL OR last_write_at < $2);

-- name: LineageUpstream :many
-- Walks dataset <- job <- dataset, one hop per job. A job with no inputs still
-- appears (input_id is 0), so sources are visible; likewise sinks downstream.
WITH RECURSIVE walk(input_id, job_id, output_id, depth) AS (
    SELECT i.dataset_id, o.job_id, o.dataset_id, 1
    FROM lineage_edges o
    LEFT JOIN lineage_edges i ON i.job_id = o.job_id AND i.direction = 'input'
    WHERE o.dataset_id = sqlc.arg(dataset_id)::bigint AND o.direction = 'output'
  UNION
    SELECT i.dataset_id, o.job_id, o.dataset_id, w.depth + 1
    FROM walk w
    JOIN lineage_edges o ON o.dataset_id = w.input_id AND o.direction = 'output'
    LEFT JOIN lineage_edges i ON i.job_id = o.job_id AND i.direction = 'input'
    WHERE w.depth < sqlc.arg(max_depth)::int
)
SELECT COALESCE(input_id, 0)::bigint AS input_id, job_id::bigint AS job_id, COALESCE(output_id, 0)::bigint AS output_id,
       min(depth)::int AS depth
FROM walk
GROUP BY input_id, job_id, output_id;

-- name: LineageDownstream :many
WITH RECURSIVE walk(input_id, job_id, output_id, depth) AS (
    SELECT i.dataset_id, i.job_id, o.dataset_id, 1
    FROM lineage_edges i
    LEFT JOIN lineage_edges o ON o.job_id = i.job_id AND o.direction = 'output'
    WHERE i.dataset_id = sqlc.arg(dataset_id)::bigint AND i.direction = 'input'
  UNION
    SELECT i.dataset_id, i.job_id, o.dataset_id, w.depth + 1
    FROM walk w
    JOIN lineage_edges i ON i.dataset_id = w.output_id AND i.direction = 'input'
    LEFT JOIN lineage_edges o ON o.job_id = i.job_id AND o.direction = 'output'
    WHERE w.depth < sqlc.arg(max_depth)::int
)
SELECT COALESCE(input_id, 0)::bigint AS input_id, job_id::bigint AS job_id, COALESCE(output_id, 0)::bigint AS output_id,
       min(depth)::int AS depth
FROM walk
GROUP BY input_id, job_id, output_id;

-- name: GetJobsByIDs :many
SELECT * FROM jobs WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: CountLineage :one
SELECT (SELECT count(*) FROM jobs)::int AS jobs, (SELECT count(*) FROM lineage_edges)::int AS edges;
