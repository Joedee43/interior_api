-- Aggregate revisions protect autosave mutations from stale tabs/devices.

-- +goose Up
ALTER TABLE designs
    ADD COLUMN revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT designs_revision_check CHECK (revision > 0);

-- +goose Down
ALTER TABLE designs
    DROP CONSTRAINT designs_revision_check,
    DROP COLUMN revision;
