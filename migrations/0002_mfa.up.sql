-- 2FA TOTP (revisi FR-17; DECISIONS T15/D25).
-- Ditambahkan sebagai migrasi terpisah agar database yang sudah menjalankan
-- 0001 tetap bisa ditingkatkan tanpa dibuat ulang.

ALTER TABLE users
  ADD COLUMN totp_secret_enc TEXT,                -- secret TOTP terenkripsi AES-GCM
  ADD COLUMN totp_enabled_at TIMESTAMPTZ,         -- NULL = belum enroll 2FA
  ADD COLUMN totp_last_step BIGINT;               -- langkah terakhir yang dipakai, cegah replay kode

-- Status setengah-jadi antara password valid dan 2FA selesai.
CREATE TABLE mfa_challenges (
  id CHAR(64) PRIMARY KEY,                        -- sha256(challenge token); token asli hanya di cookie
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose VARCHAR(10) NOT NULL CHECK (purpose IN ('LOGIN','ENROLL')),
  attempts SMALLINT NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  ip_address VARCHAR(45),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_mfa_challenges_user ON mfa_challenges(user_id);

-- Recovery code sekali pakai, disimpan sebagai hash SHA-256.
CREATE TABLE recovery_codes (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash CHAR(64) NOT NULL UNIQUE,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_recovery_codes_user ON recovery_codes(user_id);
