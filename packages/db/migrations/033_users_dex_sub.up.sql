-- Dex OIDC subject (the `sub` claim). For Dex local users this is a base64
-- protobuf string, not the users.id UUID. Nullable until backfilled on login.
ALTER TABLE users ADD COLUMN IF NOT EXISTS dex_sub TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS users_dex_sub_key
    ON users (dex_sub)
    WHERE dex_sub IS NOT NULL;
