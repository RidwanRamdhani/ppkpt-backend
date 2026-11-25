DROP TABLE IF EXISTS recovery_codes;
DROP TABLE IF EXISTS mfa_challenges;

ALTER TABLE users
  DROP COLUMN IF EXISTS totp_last_step,
  DROP COLUMN IF EXISTS totp_enabled_at,
  DROP COLUMN IF EXISTS totp_secret_enc;
