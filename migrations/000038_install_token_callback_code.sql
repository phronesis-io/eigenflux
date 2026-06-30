-- +goose Up
-- +goose StatementBegin
-- callback_code records the outcome of the platform conversion callback (Loop B)
-- so success/failure is visible per ref in the growth dashboard:
--   -1 = not yet attempted (default)
--    0 = the ad platform accepted the conversion (success)
--   >0 = platform business error (e.g. 400007 rate limited, auth error)
--   -2 = transport/token error (network, getAccessToken failure)
-- A non-zero code is re-claimable, so a later trigger (the install report) retries
-- a callback the /r/ fetch couldn't complete; code 0 is terminal (exactly-once).
ALTER TABLE install_tokens ADD COLUMN callback_code INT NOT NULL DEFAULT -1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN callback_code;
-- +goose StatementEnd
