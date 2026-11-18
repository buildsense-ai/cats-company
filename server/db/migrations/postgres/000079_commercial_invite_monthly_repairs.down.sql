DO $$
BEGIN
    LOCK TABLE commercial_invite_monthly_repairs IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM commercial_invite_monthly_repairs) THEN
        RAISE EXCEPTION 'cannot roll back recorded invite monthly repairs';
    END IF;
END $$;
DROP TABLE commercial_invite_monthly_repairs;
