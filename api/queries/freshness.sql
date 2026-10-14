-- name: ListSLODatasets :many
SELECT id, namespace, name, tier, owner_team, freshness_slo_seconds, last_write_at,
       slo_status, created_at
FROM datasets
WHERE registered AND freshness_slo_seconds IS NOT NULL;

-- name: SetSLOStatus :exec
UPDATE datasets SET slo_status = $2, slo_status_since = $3 WHERE id = $1;

-- name: InsertSLOTransition :exec
INSERT INTO slo_transitions (dataset_id, from_status, to_status, staleness_ms, deadline_at, detected_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListSLOTransitions :many
SELECT * FROM slo_transitions WHERE dataset_id = $1 ORDER BY detected_at DESC LIMIT sqlc.arg(row_limit);

-- name: ListBreachTransitionsSince :many
SELECT t.*, d.name
FROM slo_transitions t JOIN datasets d ON d.id = t.dataset_id
WHERE t.to_status = 'breached' AND t.detected_at >= $1
ORDER BY t.detected_at;
