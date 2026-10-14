-- name: CreateChangeRequest :one
INSERT INTO change_requests (dataset_id, schema_version_id, from_version, state, report, opened_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetChangeRequest :one
SELECT cr.*, sv.version AS to_version, d.namespace, d.name, d.owner_team
FROM change_requests cr
JOIN schema_versions sv ON sv.id = cr.schema_version_id
JOIN datasets d ON d.id = cr.dataset_id
WHERE cr.id = $1;

-- name: LockChangeRequest :one
SELECT * FROM change_requests WHERE id = $1 FOR UPDATE;

-- name: ListChangeRequests :many
SELECT cr.*, sv.version AS to_version, d.namespace, d.name, d.owner_team,
       (SELECT count(*) FROM change_request_reviews r
         WHERE r.change_request_id = cr.id AND r.decision IS NULL)::int AS pending_reviews
FROM change_requests cr
JOIN schema_versions sv ON sv.id = cr.schema_version_id
JOIN datasets d ON d.id = cr.dataset_id
WHERE (sqlc.narg(dataset_id)::bigint IS NULL OR cr.dataset_id = sqlc.narg(dataset_id))
  AND (sqlc.narg(state)::text IS NULL OR cr.state = sqlc.narg(state))
  AND (sqlc.narg(consumer_team)::text IS NULL OR EXISTS (
        SELECT 1 FROM change_request_reviews r
        WHERE r.change_request_id = cr.id AND r.consumer_team = sqlc.narg(consumer_team)))
ORDER BY cr.opened_at DESC
LIMIT 200;

-- name: AddReview :exec
INSERT INTO change_request_reviews (change_request_id, consumer_team) VALUES ($1, $2);

-- name: ListReviews :many
SELECT * FROM change_request_reviews WHERE change_request_id = $1 ORDER BY consumer_team;

-- name: DecideReview :execrows
UPDATE change_request_reviews
SET decision = $3, reason = $4, decided_by = $5, decided_at = now()
WHERE change_request_id = $1 AND consumer_team = $2 AND decision IS NULL;

-- name: CountUndecidedReviews :one
SELECT count(*)::int FROM change_request_reviews
WHERE change_request_id = $1 AND decision IS NULL;

-- name: SetChangeRequestState :exec
UPDATE change_requests
SET state = $2,
    closed_at = CASE WHEN $2 IN ('published', 'rejected', 'withdrawn') THEN now() ELSE closed_at END
WHERE id = $1;
