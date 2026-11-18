ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ppkpt_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO ppkpt_app;

CREATE TABLE users (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  username VARCHAR(50) NOT NULL UNIQUE,
  email VARCHAR(120) NOT NULL UNIQUE,
  password_hash VARCHAR(100) NOT NULL,
  role VARCHAR(20) NOT NULL CHECK (role IN ('SATGAS','PEJABAT')),
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  must_change_password BOOLEAN NOT NULL DEFAULT TRUE,
  failed_login_count SMALLINT NOT NULL DEFAULT 0,
  locked_until TIMESTAMPTZ,
  last_login_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id CHAR(64) PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  ip_address VARCHAR(45),
  user_agent VARCHAR(255)
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE violation_types (
  id SMALLINT PRIMARY KEY,
  code VARCHAR(30) NOT NULL UNIQUE,
  name VARCHAR(60) NOT NULL
);
INSERT INTO violation_types (id, code, name) VALUES
  (1,'FISIK','Fisik'),(2,'PSIKIS','Psikis'),(3,'PERUNDUNGAN','Perundungan'),
  (4,'SEKSUAL','Seksual'),(5,'DISKRIMINASI_INTOLERANSI','Diskriminasi dan intoleransi'),
  (6,'KEBIJAKAN_KEKERASAN','Kebijakan yang mengandung kekerasan');

CREATE TABLE reports (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  case_number CHAR(8) NOT NULL UNIQUE,
  pin_hash VARCHAR(100) NOT NULL,
  reporter_confidential BOOLEAN NOT NULL DEFAULT FALSE,
  channel VARCHAR(20) NOT NULL CHECK (channel IN ('PORTAL','HOTLINE','TATAP_MUKA','RUJUKAN')),
  referral_unit VARCHAR(100),
  status VARCHAR(25) NOT NULL DEFAULT 'DILAPORKAN' CHECK (status IN
    ('DILAPORKAN','PERLU_DILENGKAPI','PENELAAHAN','PEMERIKSAAN','REKOMENDASI','KEPUTUSAN','PEMANTAUAN','SELESAI')),
  case_condition VARCHAR(15) CHECK (case_condition IN ('DARURAT','TIDAK_DARURAT')),
  reporter_is_victim BOOLEAN NOT NULL,
  chronology TEXT NOT NULL,
  additional_description TEXT,
  reported_elsewhere BOOLEAN NOT NULL DEFAULT FALSE,
  reported_to VARCHAR(150),
  efforts_made TEXT,
  expectation TEXT,
  consent_at TIMESTAMPTZ,
  consent_text_version VARCHAR(20),
  assigned_to BIGINT REFERENCES users(id),
  entered_by BIGINT REFERENCES users(id),
  received_at TIMESTAMPTZ,
  pin_failed_count SMALLINT NOT NULL DEFAULT 0,
  pin_locked_until TIMESTAMPTZ,
  review_started_at TIMESTAMPTZ, review_due_at TIMESTAMPTZ,
  exam_started_at TIMESTAMPTZ,   exam_due_at TIMESTAMPTZ,
  closed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (channel <> 'PORTAL' OR consent_at IS NOT NULL)
);
CREATE INDEX idx_reports_status ON reports(status);
CREATE INDEX idx_reports_created_at ON reports(created_at);
CREATE INDEX idx_reports_assigned_to ON reports(assigned_to);

CREATE TABLE reporters (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL UNIQUE REFERENCES reports(id) ON DELETE CASCADE,
  name VARCHAR(100) NOT NULL,
  age SMALLINT, gender VARCHAR(15), id_number VARCHAR(30),
  category VARCHAR(30) CHECK (category IN ('MAHASISWA','DOSEN','TENAGA_KEPENDIDIKAN','LAINNYA')),
  institution VARCHAR(100), faculty VARCHAR(100), program VARCHAR(100),
  phone VARCHAR(20), email VARCHAR(120)
);

CREATE TABLE parties (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  party_type VARCHAR(10) NOT NULL CHECK (party_type IN ('KORBAN','TERLAPOR','SAKSI','AHLI')),
  name VARCHAR(100),
  gender VARCHAR(15), id_number VARCHAR(30), category VARCHAR(30),
  institution VARCHAR(100), faculty VARCHAR(100), program VARCHAR(100),
  contact VARCHAR(120), relation_to_other VARCHAR(100),
  added_by BIGINT REFERENCES users(id)
);
CREATE INDEX idx_parties_report ON parties(report_id);

CREATE TABLE incidents (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  date_start DATE NOT NULL, date_end DATE,
  description TEXT NOT NULL, location VARCHAR(200) NOT NULL,
  CHECK (date_end IS NULL OR date_end >= date_start)
);

CREATE TABLE report_violation_types (
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  violation_type_id SMALLINT NOT NULL REFERENCES violation_types(id),
  PRIMARY KEY (report_id, violation_type_id)
);

CREATE TABLE evidences (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  category VARCHAR(15) NOT NULL CHECK (category IN ('BUKTI','BAI','LAMPIRAN')),
  original_name VARCHAR(255) NOT NULL,
  stored_name VARCHAR(100) NOT NULL UNIQUE,
  mime_type VARCHAR(100) NOT NULL,
  size_bytes BIGINT NOT NULL,
  uploaded_by BIGINT REFERENCES users(id),
  uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_evidences_report ON evidences(report_id);

CREATE TABLE verifications (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  member_id BIGINT NOT NULL REFERENCES users(id),
  result VARCHAR(20) NOT NULL CHECK (result IN ('LENGKAP','PERLU_DILENGKAPI')),
  note_for_reporter TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE review_notes (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL UNIQUE REFERENCES reports(id) ON DELETE CASCADE,
  member_id BIGINT NOT NULL REFERENCES users(id),
  victim_identified TEXT, witness_identified TEXT, initial_evidence_note TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE examination_sessions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  examiner_id BIGINT NOT NULL REFERENCES users(id),
  session_date DATE NOT NULL, summary TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE session_participants (
  session_id BIGINT NOT NULL REFERENCES examination_sessions(id),
  party_id BIGINT NOT NULL REFERENCES parties(id),
  role VARCHAR(20) NOT NULL,
  PRIMARY KEY (session_id, party_id)
);

CREATE TABLE referrals (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  service_type VARCHAR(20) NOT NULL CHECK (service_type IN ('PSIKOLOGIS','HUKUM','MEDIS','PERLINDUNGAN')),
  target_party_id BIGINT REFERENCES parties(id),
  institution VARCHAR(150) NOT NULL,
  referral_date DATE NOT NULL,
  follow_up_status VARCHAR(30) NOT NULL DEFAULT 'DIAJUKAN',
  notes TEXT,
  created_by BIGINT NOT NULL REFERENCES users(id)
);

CREATE TABLE recovery_monitorings (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  type VARCHAR(20) NOT NULL CHECK (type IN ('PEMANTAUAN','EVALUASI','PENCEGAHAN')),
  note TEXT NOT NULL, recorded_at DATE NOT NULL,
  created_by BIGINT NOT NULL REFERENCES users(id)
);

CREATE TABLE conclusions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL UNIQUE REFERENCES reports(id) ON DELETE CASCADE,
  result VARCHAR(15) NOT NULL CHECK (result IN ('TERBUKTI','TIDAK_TERBUKTI')),
  summary TEXT NOT NULL,
  submission_status VARCHAR(10) NOT NULL DEFAULT 'DRAF' CHECK (submission_status IN ('DRAF','DIAJUKAN')),
  submitted_at TIMESTAMPTZ,
  created_by BIGINT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE recommendations (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  conclusion_id BIGINT NOT NULL REFERENCES conclusions(id),
  category VARCHAR(15) NOT NULL CHECK (category IN ('ADMINISTRATIF','AKADEMIK','KEPEGAWAIAN')),
  severity VARCHAR(10) NOT NULL CHECK (severity IN ('RINGAN','SEDANG','BERAT')),
  description TEXT
);

CREATE TABLE decisions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL UNIQUE REFERENCES reports(id) ON DELETE CASCADE,
  official_id BIGINT NOT NULL REFERENCES users(id),
  decision_type VARCHAR(15) NOT NULL CHECK (decision_type IN ('SANKSI','TIDAK_TERBUKTI')),
  sanction_detail TEXT,
  decided_at DATE NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE result_deliveries (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  delivered_to VARCHAR(150) NOT NULL, delivered_at DATE NOT NULL,
  name_restored BOOLEAN NOT NULL DEFAULT FALSE,
  note TEXT,
  created_by BIGINT NOT NULL REFERENCES users(id)
);

CREATE TABLE status_history (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  from_status VARCHAR(25), to_status VARCHAR(25) NOT NULL,
  changed_by BIGINT REFERENCES users(id),
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_status_history_report ON status_history(report_id);

CREATE TABLE audit_logs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT REFERENCES users(id),
  actor_type VARCHAR(10) NOT NULL CHECK (actor_type IN ('USER','REPORTER','SYSTEM')),
  action VARCHAR(50) NOT NULL,
  entity VARCHAR(50), entity_id BIGINT,
  ip_address VARCHAR(45),
  detail JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_logs_created_at ON audit_logs(created_at);

REVOKE UPDATE, DELETE, TRUNCATE ON audit_logs FROM ppkpt_app;
CREATE FUNCTION audit_logs_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_logs is append-only';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_audit_logs_immutable BEFORE UPDATE OR DELETE ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION audit_logs_immutable();