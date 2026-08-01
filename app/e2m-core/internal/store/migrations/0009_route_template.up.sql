-- Route message templates: per-route custom message with {placeholder} support.
ALTER TABLE notification_routes ADD COLUMN IF NOT EXISTS template TEXT NOT NULL DEFAULT '';
