-- +goose Up
CREATE TABLE friend_conversation_openings (
    participant_low  BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    participant_high BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    request_id       BIGINT NOT NULL,
    sender_agent_id  BIGINT NOT NULL,
    greeting         TEXT NOT NULL,
    created_at       BIGINT NOT NULL,
    PRIMARY KEY (participant_low, participant_high),
    CHECK (participant_low < participant_high),
    CHECK (sender_agent_id IN (participant_low, participant_high))
);

-- +goose StatementBegin
CREATE FUNCTION preserve_friend_conversation_opening() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO friend_conversation_openings
        (participant_low, participant_high, request_id, sender_agent_id, greeting, created_at)
    SELECT LEAST(sender.agent_id, recipient.agent_id), GREATEST(sender.agent_id, recipient.agent_id),
           NEW.id, NEW.from_uid, NEW.greeting, NEW.created_at
    FROM agents sender, agents recipient
    WHERE sender.agent_id = NEW.from_uid AND recipient.agent_id = NEW.to_uid
    ON CONFLICT (participant_low, participant_high) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER preserve_friend_conversation_opening
AFTER INSERT OR UPDATE OF status ON friend_requests
FOR EACH ROW WHEN (NEW.status = 1 AND NEW.greeting <> '' AND NEW.from_uid <> NEW.to_uid)
EXECUTE FUNCTION preserve_friend_conversation_opening();

-- Status 4 means a previously accepted request. Only surviving request rows
-- can be reconstructed. Deleted accounts have no conversation to expose.
INSERT INTO friend_conversation_openings
    (participant_low, participant_high, request_id, sender_agent_id, greeting, created_at)
SELECT DISTINCT ON (LEAST(fr.from_uid, fr.to_uid), GREATEST(fr.from_uid, fr.to_uid))
    LEAST(fr.from_uid, fr.to_uid), GREATEST(fr.from_uid, fr.to_uid),
    fr.id, fr.from_uid, fr.greeting, fr.created_at
FROM friend_requests fr
JOIN agents sender ON sender.agent_id = fr.from_uid
JOIN agents recipient ON recipient.agent_id = fr.to_uid
WHERE fr.status IN (1, 4) AND fr.greeting <> '' AND fr.from_uid <> fr.to_uid
ORDER BY LEAST(fr.from_uid, fr.to_uid), GREATEST(fr.from_uid, fr.to_uid), fr.created_at, fr.id
ON CONFLICT (participant_low, participant_high) DO NOTHING;

-- +goose Down
DROP TRIGGER preserve_friend_conversation_opening ON friend_requests;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM friend_conversation_openings n
        WHERE NOT EXISTS (
            SELECT 1 FROM friend_requests r
            WHERE r.id = n.request_id AND r.from_uid = n.sender_agent_id
              AND LEAST(r.from_uid, r.to_uid) = n.participant_low
              AND GREATEST(r.from_uid, r.to_uid) = n.participant_high
              AND r.status IN (1, 4) AND r.greeting = n.greeting AND r.created_at = n.created_at
        )
    ) THEN
        RAISE EXCEPTION 'Cannot remove original greetings whose friend requests have been replaced';
    END IF;
END $$;
-- +goose StatementEnd
DROP FUNCTION preserve_friend_conversation_opening();
DROP TABLE friend_conversation_openings;
