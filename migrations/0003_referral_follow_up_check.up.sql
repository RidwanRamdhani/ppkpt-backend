-- Deviasi SDD (DECISIONS D26): enum `referrals.follow_up_status` semula hanya
-- divalidasi di aplikasi karena kolomnya dibuat tanpa CHECK. Constraint ini
-- menutup celah bila ada jalur tulis lain (migrasi data, konsol, script)
-- menyisipkan nilai di luar enum. Nilai mengikuti yang dipakai aplikasi.
ALTER TABLE referrals
  ADD CONSTRAINT referrals_follow_up_status_check
  CHECK (follow_up_status IN ('DIAJUKAN', 'DIPROSES', 'SELESAI'));
