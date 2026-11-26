-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE agents DROP CONSTRAINT chk_agents_identity_state;
ALTER TABLE agents ADD CONSTRAINT chk_agents_identity_state
    CHECK (identity_state IN ('active', 'recovered_temporary', 'deleted')) NOT VALID;

CREATE INDEX idx_agents_deleted_identity ON agents(agent_id) WHERE identity_state = 'deleted';

CREATE TABLE account_soft_delete_audit (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id),
    operator_name TEXT NOT NULL CHECK (operator_name <> ''),
    email_digest BYTEA NOT NULL CHECK (octet_length(email_digest) = 32),
    ticket TEXT NOT NULL CHECK (btrim(ticket) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Provision this NOLOGIN group separately. Migrations need no CREATEROLE.
CREATE FUNCTION eigenflux_can_soft_delete_account() RETURNS boolean
LANGUAGE SQL STABLE SET search_path = pg_catalog AS $$
    WITH RECURSIVE memberships(oid) AS (
        SELECT oid FROM pg_roles WHERE rolname = session_user
        UNION
        SELECT m.roleid FROM pg_auth_members m JOIN memberships r ON m.member = r.oid
    )
    SELECT EXISTS (
        SELECT FROM memberships JOIN pg_roles USING (oid)
        WHERE rolname = 'eigenflux_account_eraser'
    )
$$;

-- +goose StatementBegin
CREATE FUNCTION guard_account_soft_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.eigenflux_can_soft_delete_account() THEN
        RAISE EXCEPTION 'account erasure operator membership required' USING ERRCODE = '42501';
    END IF;
    IF TG_TABLE_NAME = 'account_soft_delete_audit' THEN
        NEW.operator_name := session_user;
        NEW.created_at := clock_timestamp();
    ELSE
        IF TG_OP = 'UPDATE' AND OLD.identity_state = 'deleted' THEN
            RAISE EXCEPTION 'deleted identity is terminal' USING ERRCODE = '23514';
        END IF;
        IF NOT EXISTS (SELECT FROM public.account_soft_delete_audit WHERE agent_id = NEW.agent_id) THEN
            RAISE EXCEPTION 'account soft deletion requires its audit record' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER guard_insert_deleted_identity BEFORE INSERT ON agents
    FOR EACH ROW WHEN (NEW.identity_state = 'deleted')
    EXECUTE FUNCTION guard_account_soft_delete();
CREATE TRIGGER guard_change_deleted_identity BEFORE UPDATE OF identity_state ON agents
    FOR EACH ROW WHEN (OLD.identity_state IS DISTINCT FROM NEW.identity_state
        AND (OLD.identity_state = 'deleted' OR NEW.identity_state = 'deleted'))
    EXECUTE FUNCTION guard_account_soft_delete();

CREATE TRIGGER guard_account_soft_delete_audit BEFORE INSERT ON account_soft_delete_audit
    FOR EACH ROW EXECUTE FUNCTION guard_account_soft_delete();

-- +goose StatementBegin
CREATE FUNCTION protect_account_soft_delete_audit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'account erasure audit is append-only' USING ERRCODE = '42501';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER protect_account_soft_delete_audit BEFORE UPDATE OR DELETE OR TRUNCATE ON account_soft_delete_audit
    FOR EACH STATEMENT EXECUTE FUNCTION protect_account_soft_delete_audit();

-- +goose Down
-- Restoring the constraint first causes rollback to fail safely if deleted accounts exist.
ALTER TABLE agents DROP CONSTRAINT chk_agents_identity_state;
ALTER TABLE agents ADD CONSTRAINT chk_agents_identity_state
    CHECK (identity_state IN ('active', 'recovered_temporary'));
DROP TRIGGER guard_change_deleted_identity ON agents;
DROP TRIGGER guard_insert_deleted_identity ON agents;
DROP TABLE account_soft_delete_audit;
DROP INDEX idx_agents_deleted_identity;
DROP FUNCTION protect_account_soft_delete_audit();
DROP FUNCTION guard_account_soft_delete();
DROP FUNCTION eigenflux_can_soft_delete_account();
