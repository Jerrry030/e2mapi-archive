UPDATE system_settings
SET value = jsonb_set(value, '{registration_email_suffix_whitelist}', '[]'::jsonb, true),
    updated_at = now()
WHERE key = 'auth.registration'
  AND (
    (value->'registration_email_suffix_whitelist') IS NULL
    OR value->'registration_email_suffix_whitelist' = 'null'::jsonb
  );
