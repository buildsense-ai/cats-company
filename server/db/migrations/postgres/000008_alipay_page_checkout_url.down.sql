DO $$ BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'checkout_url'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'code_url'
    ) THEN
        ALTER TABLE commercial_orders RENAME COLUMN checkout_url TO code_url;
    ELSIF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'checkout_url'
    ) AND EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'code_url'
    ) THEN
        UPDATE commercial_orders
        SET code_url = checkout_url
        WHERE code_url = '' AND checkout_url <> '';
        ALTER TABLE commercial_orders DROP COLUMN checkout_url;
    ELSIF NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'code_url'
    ) THEN
        ALTER TABLE commercial_orders ADD COLUMN code_url TEXT NOT NULL DEFAULT '';
    END IF;
END $$;
