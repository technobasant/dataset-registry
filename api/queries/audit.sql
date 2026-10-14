-- name: InsertAudit :exec
INSERT INTO audit_log (actor, action, entity_type, entity_id, details)
VALUES ($1, $2, $3, $4, $5);

-- name: ListAudit :many
SELECT * FROM audit_log
WHERE (sqlc.narg(entity_type)::text IS NULL OR entity_type = sqlc.narg(entity_type))
  AND (sqlc.narg(entity_id)::bigint IS NULL OR entity_id = sqlc.narg(entity_id))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);
