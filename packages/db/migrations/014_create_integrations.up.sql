CREATE TYPE integration_type AS ENUM ('paperless', 'vaultwarden', 'homeassistant', 'minio');
CREATE TYPE integration_status AS ENUM ('connected', 'disconnected', 'error');

CREATE TABLE integrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    type integration_type NOT NULL,
    config BYTEA,
    status integration_status NOT NULL DEFAULT 'disconnected',
    last_health_check TIMESTAMPTZ,
    last_sync TIMESTAMPTZ,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(household_id, type)
);
