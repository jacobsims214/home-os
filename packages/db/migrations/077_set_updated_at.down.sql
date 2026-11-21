-- 077_set_updated_at.down.sql (#5508)
--
-- Back to 074: the shared trigger and its function are gone, and the three
-- bespoke per-table versions that 046/047/049 own are put back exactly as
-- those migrations left them, so the schema is byte-for-byte what it was
-- before 077.
--
-- The 25 triggers are dropped by catalogue rather than by a hand-written
-- list, for the same reason the up migration attaches them that way. Every
-- statement is IF EXISTS: this down also runs inside the older migrations'
-- test chains (050 down, 066 down, ...), which may have dropped some of
-- these tables already.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. The shared implementation, off every table it was attached to
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    v_table TEXT;
BEGIN
    FOR v_table IN
        SELECT c.table_name
          FROM information_schema.columns c
          JOIN information_schema.tables t
            ON t.table_schema = c.table_schema
           AND t.table_name   = c.table_name
           AND t.table_type   = 'BASE TABLE'
         WHERE c.table_schema = 'public'
           AND c.column_name  = 'updated_at'
    LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I',
                       v_table || '_set_updated_at', v_table);
    END LOOP;
END
$$;

DROP FUNCTION IF EXISTS set_updated_at();

-- ---------------------------------------------------------------------------
-- 2. The three per-table versions 046/047/049 had, restored
-- ---------------------------------------------------------------------------

-- lists (046): lists_touch_updated_at
CREATE FUNCTION lists_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

DO $$
BEGIN
    IF to_regclass('public.lists') IS NOT NULL THEN
        CREATE TRIGGER lists_touch_updated_at
            BEFORE UPDATE ON lists
            FOR EACH ROW EXECUTE FUNCTION lists_touch_updated_at();
    ELSE
        DROP FUNCTION lists_touch_updated_at();
    END IF;
END
$$;

-- recipes (047): recipes_touch_updated_at
CREATE FUNCTION recipes_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

DO $$
BEGIN
    IF to_regclass('public.recipes') IS NOT NULL THEN
        CREATE TRIGGER recipes_touch_updated_at
            BEFORE UPDATE ON recipes
            FOR EACH ROW EXECUTE FUNCTION recipes_touch_updated_at();
    ELSE
        DROP FUNCTION recipes_touch_updated_at();
    END IF;
END
$$;

-- guides (049): guides_touch_updated_at
CREATE FUNCTION guides_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

DO $$
BEGIN
    IF to_regclass('public.guides') IS NOT NULL THEN
        CREATE TRIGGER guides_touch_updated_at
            BEFORE UPDATE ON guides
            FOR EACH ROW EXECUTE FUNCTION guides_touch_updated_at();
    ELSE
        DROP FUNCTION guides_touch_updated_at();
    END IF;
END
$$;

COMMIT;
