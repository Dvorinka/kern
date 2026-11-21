-- +goose Up
-- HA-safe GHA-protocol cache uploads (#369): reserve state and chunk
-- inventory live in Postgres so any API replica can serve PATCH/commit.
-- Chunks are immutable blob objects keyed by offset; commit concatenates.

CREATE TABLE gh_cache_uploads (
    id         bigserial PRIMARY KEY,
    job_id     uuid NOT NULL,
    key        text NOT NULL,
    version    text NOT NULL,
    scope      text NOT NULL,
    blob_ref   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gh_cache_uploads_created ON gh_cache_uploads(created_at);

CREATE TABLE gh_cache_upload_chunks (
    upload_id bigint NOT NULL REFERENCES gh_cache_uploads(id) ON DELETE CASCADE,
    start     bigint NOT NULL,
    stop      bigint NOT NULL,
    blob_ref  text NOT NULL,
    PRIMARY KEY (upload_id, start)
);

-- +goose Down
DROP TABLE gh_cache_upload_chunks;
DROP TABLE gh_cache_uploads;
