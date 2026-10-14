-- name: CreateDataset :one
-- Registering a dataset that lineage already discovered claims it; registering
-- one that someone already owns returns no row (the caller reports a conflict).
INSERT INTO datasets (namespace, name, description, registered, owner_team, owner_person,
                      tier, freshness_slo_seconds, compatibility)
VALUES ($1, $2, $3, TRUE, $4, $5, $6, $7, $8)
ON CONFLICT (namespace, name) DO UPDATE
SET description = EXCLUDED.description,
    registered = TRUE,
    owner_team = EXCLUDED.owner_team,
    owner_person = EXCLUDED.owner_person,
    tier = EXCLUDED.tier,
    freshness_slo_seconds = EXCLUDED.freshness_slo_seconds,
    compatibility = EXCLUDED.compatibility,
    updated_at = now()
WHERE datasets.registered = FALSE
RETURNING *;

-- name: GetDataset :one
SELECT * FROM datasets WHERE id = $1;

-- name: GetDatasetForUpdate :one
SELECT * FROM datasets WHERE id = $1 FOR UPDATE;

-- name: GetDatasetByName :one
SELECT * FROM datasets WHERE namespace = $1 AND name = $2;

-- name: ListDatasets :many
SELECT d.*,
       (SELECT count(*) FROM consumers c WHERE c.dataset_id = d.id)::int AS consumer_count,
       COALESCE((SELECT max(s.version) FROM schema_versions s
         WHERE s.dataset_id = d.id AND s.status = 'published'), 0)::int AS current_version,
       COALESCE((SELECT cr.id FROM change_requests cr
         WHERE cr.dataset_id = d.id AND cr.state IN ('open', 'approved')), 0)::bigint AS open_change_request
FROM datasets d
WHERE (sqlc.narg(registered)::boolean IS NULL OR d.registered = sqlc.narg(registered))
  AND (sqlc.narg(slo_status)::text IS NULL OR d.slo_status = sqlc.narg(slo_status))
  AND (sqlc.narg(owner_team)::text IS NULL OR d.owner_team = sqlc.narg(owner_team))
ORDER BY d.registered DESC, d.namespace, d.name
LIMIT sqlc.arg(row_limit);

-- name: UpdateDataset :one
UPDATE datasets
SET description = COALESCE(sqlc.narg(description), description),
    owner_team = COALESCE(sqlc.narg(owner_team), owner_team),
    owner_person = COALESCE(sqlc.narg(owner_person), owner_person),
    tier = COALESCE(sqlc.narg(tier), tier),
    freshness_slo_seconds = CASE WHEN sqlc.arg(clear_slo)::boolean THEN NULL
                                 ELSE COALESCE(sqlc.narg(freshness_slo_seconds), freshness_slo_seconds) END,
    compatibility = COALESCE(sqlc.narg(compatibility), compatibility),
    slo_status = CASE WHEN sqlc.arg(clear_slo)::boolean THEN 'unknown' ELSE slo_status END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteDataset :execrows
DELETE FROM datasets WHERE id = $1;

-- name: CountDatasets :one
SELECT count(*) FILTER (WHERE registered)::int AS registered,
       count(*) FILTER (WHERE NOT registered)::int AS unowned
FROM datasets;

-- name: GetDatasetsByIDs :many
SELECT * FROM datasets WHERE id = ANY(sqlc.arg(ids)::bigint[]);
