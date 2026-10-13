-- +goose Up
CREATE TABLE need_embedding_jobs (
    need_input_id BIGINT NOT NULL REFERENCES need_inputs(need_input_id) ON DELETE CASCADE,
    generation TEXT NOT NULL,
    next_attempt_at BIGINT NOT NULL DEFAULT 0,
    lease_until BIGINT NOT NULL DEFAULT 0,
    lease_token TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    ready BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (need_input_id, generation)
);
CREATE INDEX idx_need_embedding_jobs_due ON need_embedding_jobs(generation, next_attempt_at, lease_until);

-- +goose Down
DROP TABLE need_embedding_jobs;
