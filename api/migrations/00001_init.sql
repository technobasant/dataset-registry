-- +goose Up
-- +goose StatementBegin

-- A dataset is identified the way OpenLineage identifies it: (namespace, name).
-- `registered` is false for datasets that only ever appeared in lineage: they
-- exist, something writes them, and nobody owns them. Those are the point.
CREATE TABLE datasets (
    id                    BIGSERIAL PRIMARY KEY,
    namespace             TEXT        NOT NULL,
    name                  TEXT        NOT NULL,
    description           TEXT        NOT NULL DEFAULT '',
    registered            BOOLEAN     NOT NULL DEFAULT TRUE,
    owner_team            TEXT,
    owner_person          TEXT,
    tier                  TEXT        NOT NULL DEFAULT 'bronze'
                          CHECK (tier IN ('gold', 'silver', 'bronze')),
    freshness_slo_seconds INTEGER     CHECK (freshness_slo_seconds IS NULL OR freshness_slo_seconds > 0),
    compatibility         TEXT        NOT NULL DEFAULT 'FULL'
                          CHECK (compatibility IN ('BACKWARD', 'FORWARD', 'FULL', 'NONE')),
    last_write_at         TIMESTAMPTZ,
    last_write_run        UUID,
    slo_status            TEXT        NOT NULL DEFAULT 'unknown'
                          CHECK (slo_status IN ('unknown', 'healthy', 'at_risk', 'breached')),
    slo_status_since      TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, name),
    CHECK (NOT registered OR (owner_team IS NOT NULL AND owner_person IS NOT NULL))
);

CREATE TABLE schema_versions (
    id          BIGSERIAL PRIMARY KEY,
    dataset_id  BIGINT      NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    version     INTEGER     NOT NULL CHECK (version > 0),
    schema      JSONB       NOT NULL,
    status      TEXT        NOT NULL
                CHECK (status IN ('published', 'pending', 'rejected', 'withdrawn')),
    created_by  TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (dataset_id, version)
);

CREATE TABLE consumers (
    id          BIGSERIAL PRIMARY KEY,
    dataset_id  BIGINT      NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    team        TEXT        NOT NULL,
    contact     TEXT        NOT NULL,
    purpose     TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (dataset_id, team)
);

-- A breaking schema change waits here until every consumer registered at the
-- moment it was opened has acknowledged it.
CREATE TABLE change_requests (
    id                BIGSERIAL PRIMARY KEY,
    dataset_id        BIGINT      NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    schema_version_id BIGINT      NOT NULL UNIQUE REFERENCES schema_versions (id),
    from_version      INTEGER     NOT NULL,
    state             TEXT        NOT NULL
                      CHECK (state IN ('open', 'approved', 'published', 'rejected', 'withdrawn')),
    report            JSONB       NOT NULL,
    opened_by         TEXT        NOT NULL,
    opened_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at         TIMESTAMPTZ
);

-- Only one open request per dataset: a second breaking change waits for the first.
CREATE UNIQUE INDEX change_requests_one_open
    ON change_requests (dataset_id) WHERE state IN ('open', 'approved');

CREATE TABLE change_request_reviews (
    change_request_id BIGINT      NOT NULL REFERENCES change_requests (id) ON DELETE CASCADE,
    consumer_team     TEXT        NOT NULL,
    decision          TEXT        CHECK (decision IN ('acknowledged', 'rejected')),
    reason            TEXT        NOT NULL DEFAULT '',
    decided_by        TEXT,
    decided_at        TIMESTAMPTZ,
    PRIMARY KEY (change_request_id, consumer_team)
);

CREATE TABLE jobs (
    id            BIGSERIAL PRIMARY KEY,
    namespace     TEXT        NOT NULL,
    name          TEXT        NOT NULL,
    last_run_id   UUID,
    last_state    TEXT,
    last_event_at TIMESTAMPTZ,
    UNIQUE (namespace, name)
);

-- dataset --input--> job --output--> dataset. Lineage is the union of every
-- edge ever observed; first/last seen keep it honest about age.
CREATE TABLE lineage_edges (
    job_id      BIGINT      NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    dataset_id  BIGINT      NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    direction   TEXT        NOT NULL CHECK (direction IN ('input', 'output')),
    first_seen  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (job_id, dataset_id, direction)
);
CREATE INDEX lineage_edges_dataset ON lineage_edges (dataset_id, direction);
CREATE INDEX lineage_edges_job ON lineage_edges (job_id, direction);

CREATE TABLE slo_transitions (
    id               BIGSERIAL PRIMARY KEY,
    dataset_id       BIGINT      NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    from_status      TEXT        NOT NULL,
    to_status        TEXT        NOT NULL,
    staleness_ms     BIGINT      NOT NULL,
    deadline_at      TIMESTAMPTZ,
    detected_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX slo_transitions_dataset ON slo_transitions (dataset_id, detected_at);

-- Append-only. The triggers below refuse UPDATE, DELETE and TRUNCATE, so the
-- record of who approved what cannot be edited after the fact by the app.
CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor       TEXT        NOT NULL,
    action      TEXT        NOT NULL,
    entity_type TEXT        NOT NULL,
    entity_id   BIGINT      NOT NULL,
    details     JSONB       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX audit_log_entity ON audit_log (entity_type, entity_id, at);

CREATE FUNCTION audit_log_is_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (% refused)', TG_OP;
END;
$$;

CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_is_append_only();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit_log, slo_transitions, lineage_edges, jobs,
    change_request_reviews, change_requests, consumers, schema_versions, datasets CASCADE;
DROP FUNCTION IF EXISTS audit_log_is_append_only();
-- +goose StatementEnd
