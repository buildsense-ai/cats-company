DO $$ BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'code_url'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'checkout_url'
    ) THEN
        ALTER TABLE commercial_orders RENAME COLUMN code_url TO checkout_url;
    ELSIF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'code_url'
    ) AND EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'checkout_url'
    ) THEN
        UPDATE commercial_orders
        SET checkout_url = code_url
        WHERE checkout_url = '' AND code_url <> '';
        ALTER TABLE commercial_orders DROP COLUMN code_url;
    ELSIF NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_orders'
          AND column_name = 'checkout_url'
    ) THEN
        ALTER TABLE commercial_orders ADD COLUMN checkout_url TEXT NOT NULL DEFAULT '';
    END IF;
END $$;
