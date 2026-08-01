ALTER TABLE supply_usage_records
    ADD COLUMN IF NOT EXISTS settlement_reason TEXT NOT NULL DEFAULT '';
