-- +goose Up
-- Worker-slot count (#372): runners report the concurrency they were
-- started with so ClaimJob can yield to peers with free slots and
-- FleetSlots can size split:auto from real capacity, not CPU count.
ALTER TABLE runners ADD COLUMN concurrency int NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE runners DROP COLUMN concurrency;
