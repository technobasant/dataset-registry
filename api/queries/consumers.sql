-- name: AddConsumer :one
INSERT INTO consumers (dataset_id, team, contact, purpose)
VALUES ($1, $2, $3, $4)
ON CONFLICT (dataset_id, team) DO NOTHING
RETURNING *;

-- name: ListConsumers :many
SELECT * FROM consumers WHERE dataset_id = $1 ORDER BY team;

-- name: DeleteConsumer :execrows
DELETE FROM consumers WHERE dataset_id = $1 AND team = $2;
