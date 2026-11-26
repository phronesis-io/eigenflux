-- Run as the table owner after a DBA creates the NOLOGIN operations group.
BEGIN;
GRANT USAGE ON SCHEMA public TO eigenflux_account_eraser;
SET LOCAL search_path = pg_catalog, public;
GRANT SELECT (agent_id, email, email_kind, identity_state),
      UPDATE (identity_state, email, email_kind, email_verified_at, updated_at)
    ON agents TO eigenflux_account_eraser;
GRANT SELECT (agent_id, email_digest), INSERT (agent_id, email_digest, operator_name, ticket)
    ON account_soft_delete_audit TO eigenflux_account_eraser;
GRANT SELECT (agent_id, status), UPDATE (status) ON agent_sessions TO eigenflux_account_eraser;
GRANT SELECT (principal_id, revoked_at), UPDATE (revoked_at) ON agent_credential_sessions TO eigenflux_account_eraser;
GRANT SELECT (principal_id, agent_id, key_fingerprint, status), UPDATE (status, revoked_at)
    ON agent_principals TO eigenflux_account_eraser;
GRANT SELECT (agent_id, normalized_email, status), UPDATE (status, verification_state, updated_at, revoked_at)
    ON agent_email_bindings TO eigenflux_account_eraser;
GRANT SELECT (key_fingerprint, status), UPDATE (status) ON agent_bootstrap_grants TO eigenflux_account_eraser;
GRANT SELECT (subject_agent_id, console_session_id, status), UPDATE (status) ON v2_email_challenges TO eigenflux_account_eraser;
GRANT SELECT (email, status, expire_at), UPDATE (expire_at) ON auth_email_challenges TO eigenflux_account_eraser;
GRANT SELECT (agent_id, session_id, status), UPDATE (status, revoked_at) ON console_v2_sessions TO eigenflux_account_eraser;
GRANT SELECT (agent_id, consumed_at, revoked_at), UPDATE (revoked_at) ON console_v2_handoffs TO eigenflux_account_eraser;
GRANT SELECT (source_agent_id, target_agent_id, status), UPDATE (status)
    ON agent_account_recoveries, agent_cli_account_switches TO eigenflux_account_eraser;
GRANT SELECT (agent_id, status, version), UPDATE (status, version, updated_at)
    ON agent_network_goals, agent_intent_actions TO eigenflux_account_eraser;
GRANT SELECT (agent_id, active_revision), UPDATE (active_revision, updated_at) ON agent_context_heads TO eigenflux_account_eraser;
GRANT SELECT (agent_id, status), UPDATE (status) ON agent_attention_items TO eigenflux_account_eraser;
GRANT SELECT (agent_id, status), UPDATE (status, completed_at) ON agent_commands TO eigenflux_account_eraser;
GRANT SELECT (agent_id, lease_until), UPDATE (lease_until, updated_at) ON agent_runtime_leases TO eigenflux_account_eraser;
GRANT SELECT (agent_id, status), UPDATE (status) ON control_wakeup_outbox TO eigenflux_account_eraser;
GRANT SELECT (agent_id), UPDATE (recurring_publish, auto_reply_pm, auto_comment, show_add_friend, official_pm_optout, updated_at)
    ON agent_settings TO eigenflux_account_eraser;
GRANT SELECT (seller_agent_id, status) ON trading_services TO eigenflux_account_eraser;
GRANT SELECT (buyer_agent_id, seller_agent_id) ON trade_orders TO eigenflux_account_eraser;
GRANT SELECT (agent_id) ON integration_agents TO eigenflux_account_eraser;
GRANT SELECT (author_agent_id) ON raw_items TO eigenflux_account_eraser;
COMMIT;
