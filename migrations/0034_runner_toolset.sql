-- +goose Up
-- Host-toolset drift report (#344): a host executor diffs the org's
-- declared `toolset` setting against its PATH and reports the missing
-- names on every heartbeat. NULL = never reported (docker executor, or
-- toolset unset); '{}' = checked, no drift.
ALTER TABLE runners ADD COLUMN missing_tools text[];

-- +goose Down
ALTER TABLE runners DROP COLUMN missing_tools;
