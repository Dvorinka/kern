-- +goose Up
-- Optional operator-chosen glyph for a runner: an emoji or a URL/path to an
-- image. Empty keeps the default square marker.
ALTER TABLE runners
    ADD COLUMN icon text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE runners DROP COLUMN icon;
