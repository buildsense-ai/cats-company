-- Migration: Add Token project fields to users table
-- Run this on existing openchat database

USE openchat;

-- Add is_admin column
ALTER TABLE users
ADD COLUMN IF NOT EXISTS is_admin TINYINT(1) NOT NULL DEFAULT 0 AFTER pass_hash,
ADD INDEX IF NOT EXISTS idx_users_is_admin (is_admin);

-- Add balance column
ALTER TABLE users
ADD COLUMN IF NOT EXISTS balance DECIMAL(10,2) NOT NULL DEFAULT 0.00 AFTER is_admin;

-- Make email unique if not already
ALTER TABLE users
MODIFY COLUMN email VARCHAR(255) DEFAULT NULL UNIQUE;
