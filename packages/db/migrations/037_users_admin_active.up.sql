-- Instance-level admin flag (manages families/users across the whole
-- instance, distinct from household roles) and an active flag for
-- deactivating a user without deleting their data. Existing rows pick up
-- the defaults: active, non-admin.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_instance_admin BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;
