-- +goose Up
-- PostgreSQL 16 grants role creators ADMIN without INHERIT or SET. Only a
-- chain of inherited grants authorizes execution by the authenticated login.
CREATE OR REPLACE FUNCTION eigenflux_can_soft_delete_account() RETURNS boolean
LANGUAGE SQL STABLE SET search_path = pg_catalog AS $$
    WITH RECURSIVE memberships(oid) AS (
        SELECT oid FROM pg_roles WHERE rolname = session_user
        UNION
        SELECT m.roleid FROM pg_auth_members m JOIN memberships r ON m.member = r.oid
        WHERE m.inherit_option
    )
    SELECT EXISTS (
        SELECT FROM memberships JOIN pg_roles USING (oid)
        WHERE rolname = 'eigenflux_account_eraser'
    )
$$;

-- +goose Down
-- Disable the operation on rollback until a safe authorization check is installed.
CREATE OR REPLACE FUNCTION eigenflux_can_soft_delete_account() RETURNS boolean
LANGUAGE SQL STABLE SET search_path = pg_catalog AS $$ SELECT false $$;
