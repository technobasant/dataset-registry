-- name: NextSchemaVersion :one
SELECT (COALESCE(max(version), 0) + 1)::int FROM schema_versions WHERE dataset_id = $1;

-- name: CreateSchemaVersion :one
INSERT INTO schema_versions (dataset_id, version, schema, status, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: LatestPublishedSchema :one
SELECT * FROM schema_versions
WHERE dataset_id = $1 AND status = 'published'
ORDER BY version DESC
LIMIT 1;

-- name: ListSchemaVersions :many
SELECT * FROM schema_versions WHERE dataset_id = $1 ORDER BY version DESC;

-- name: GetSchemaVersion :one
SELECT * FROM schema_versions WHERE dataset_id = $1 AND version = $2;

-- name: GetSchemaVersionByID :one
SELECT * FROM schema_versions WHERE id = $1;

-- name: SetSchemaVersionStatus :exec
UPDATE schema_versions SET status = $2 WHERE id = $1;
