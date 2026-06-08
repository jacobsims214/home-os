CREATE TYPE task_status AS ENUM ('pending', 'in_progress', 'done', 'skipped');

CREATE TABLE maintenance_tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    schedule_id UUID REFERENCES maintenance_schedules(id),
    property_id UUID REFERENCES properties(id),
    asset_id UUID REFERENCES assets(id),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    status task_status NOT NULL DEFAULT 'pending',
    due_date DATE,
    completed_at TIMESTAMPTZ,
    cost NUMERIC(12,2),
    vendor_id UUID REFERENCES vendors(id),
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
