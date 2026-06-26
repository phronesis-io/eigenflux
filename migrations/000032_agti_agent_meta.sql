-- +goose Up
-- +goose StatementBegin
-- Agent self-reported identity (name + model) and the q0 warm-up "how do you
-- address your master": agent fills master_address (how it calls the human),
-- human fills human_name (their real name / preferred address) — compared on
-- the result page as the first 答题对比 row.
ALTER TABLE agti_sessions
    ADD COLUMN agent_name     VARCHAR(64)  NOT NULL DEFAULT '',
    ADD COLUMN model_name     VARCHAR(64)  NOT NULL DEFAULT '',
    ADD COLUMN master_address VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN human_name     VARCHAR(128) NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agti_sessions
    DROP COLUMN agent_name,
    DROP COLUMN model_name,
    DROP COLUMN master_address,
    DROP COLUMN human_name;
-- +goose StatementEnd
