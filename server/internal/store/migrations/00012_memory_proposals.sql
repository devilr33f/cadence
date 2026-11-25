-- +goose Up
-- a pending row that replaces an active memory is a proposal for it (approval mode): approving an 'update' writes
-- the proposal onto that memory, approving a 'delete' deletes it; a pending row without one is a new memory
ALTER TABLE memories
    ADD COLUMN replaces uuid REFERENCES memories ON DELETE CASCADE,
    ADD COLUMN proposal text CHECK (proposal IN ('update', 'delete')),
    ADD CONSTRAINT memories_proposal_target CHECK ((replaces IS NULL) = (proposal IS NULL)),
    ADD CONSTRAINT memories_proposal_pending CHECK (replaces IS NULL OR status = 'pending');

CREATE INDEX memories_replaces ON memories (replaces) WHERE replaces IS NOT NULL;

-- +goose Down
DROP INDEX memories_replaces;
ALTER TABLE memories DROP COLUMN proposal, DROP COLUMN replaces;
