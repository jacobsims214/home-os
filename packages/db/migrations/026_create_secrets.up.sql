-- Migration 026: Native secrets manager (replaces Vaultwarden for Home OS data)
--
-- Zero-knowledge design: the Go API only stores and retrieves encrypted blobs.
-- All encryption happens client-side in the browser via the Web Crypto API
-- (AES-256-GCM with PBKDF2-derived keys). See architecture/secrets-manager-research.md.
--
-- Two tables:
--   * secrets     — one row per stored secret. The encrypted_data / iv / key_version
--                   columns hold the AES-256-GCM ciphertext, 12-byte nonce, and the
--                   key version used to encrypt it. name and secret_type are kept in
--                   plaintext so the API can list/search secrets without decryption;
--                   the actual secret *data* (password, card number, note body) lives
--                   inside encrypted_data. entity_type/entity_id is the same
--                   polymorphic association pattern used by files (023) and notes (020)
--                   so a secret can be linked to any household entity, or left NULL
--                   for standalone vault entries.
--   * secret_keys — one row per (household, key_version). Stores the PBKDF2 salt and
--                   a hash of the derived key so the unlock endpoint can verify the
--                   master password without ever persisting the key itself. The
--                   UNIQUE(household_id, key_version) constraint also serves as the
--                   lookup index for "find the active key for this household".

CREATE TABLE secrets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,

    -- Polymorphic entity association (nullable for standalone vault secrets).
    -- Same pattern as files (023) and notes (020).
    entity_type     VARCHAR(50),
    entity_id       UUID,

    -- Encrypted blob — AES-256-GCM ciphertext + 12-byte nonce. Never plaintext.
    encrypted_data  BYTEA NOT NULL,
    iv              BYTEA NOT NULL,
    key_version     INT  NOT NULL DEFAULT 1,

    -- Plaintext metadata for listing/search (the secret DATA is in encrypted_data).
    name            VARCHAR(255) NOT NULL,
    secret_type     VARCHAR(50)  NOT NULL,  -- 'login', 'note', 'api_key', 'card'

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE secret_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    -- Hash of the PBKDF2-derived key (for verification) and the salt used to derive it.
    key_hash        BYTEA NOT NULL,
    key_salt        BYTEA NOT NULL,
    key_version     INT  NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- One active key per (household, version). This UNIQUE constraint doubles as the
    -- lookup index for "fetch the key row for this household + version".
    UNIQUE (household_id, key_version)
);

-- Household-scoped listing (every API request filters by household_id).
CREATE INDEX idx_secrets_household ON secrets(household_id);

-- Polymorphic entity lookup: "all secrets attached to this entity".
-- Mirrors idx_files_entity (023) / idx_notes_entity (020).
CREATE INDEX idx_secrets_entity ON secrets(entity_type, entity_id);
