package config

import (
	"encoding/base64"
	"testing"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://contoh")
	t.Setenv("S3_ENDPOINT", "localhost:9000")
	t.Setenv("S3_BUCKET", "ppkpt-evidence")
}

func TestLoad_MFA(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "dev")
	t.Setenv("MFA_ENABLED", "")
	t.Setenv("MFA_ENCRYPTION_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("dev tanpa MFA: %v", err)
	}
	if cfg.MFA.Enabled {
		t.Fatal("MFA harus nonaktif secara default di dev")
	}

	t.Setenv("MFA_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("MFA_ENABLED=true tanpa kunci harus ditolak")
	}

	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("MFA_ENCRYPTION_KEY", key)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("dengan kunci: %v", err)
	}
	if !cfg.MFA.Enabled || len(cfg.MFA.EncryptionKey) != 32 {
		t.Fatalf("MFA = %+v", cfg.MFA)
	}

	t.Setenv("MFA_ENCRYPTION_KEY", "pendek")
	if _, err := Load(); err == nil {
		t.Fatal("kunci bukan 32 byte harus ditolak")
	}
}

func TestLoad_MFAProduksiAktifSecaraDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("MFA_ENABLED", "")
	t.Setenv("MFA_ENCRYPTION_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("produksi tanpa MFA_ENCRYPTION_KEY harus gagal saat start")
	}

	t.Setenv("MFA_ENABLED", "false")
	if _, err := Load(); err == nil {
		t.Fatal("produksi dengan MFA_ENABLED=false harus ditolak")
	}
}

func TestLoad_MemakaiDefaultRateLimitDanFallbackStorageDev(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://contoh")
	t.Setenv("S3_ENDPOINT", "localhost:9000")
	t.Setenv("S3_BUCKET", "ppkpt-evidence")
	t.Setenv("S3_ACCESS_KEY", "")
	t.Setenv("S3_SECRET_KEY", "")
	t.Setenv("MINIO_ROOT_USER", "root-dev")
	t.Setenv("MINIO_ROOT_PASSWORD", "rahasia-dev")
	t.Setenv("RATE_LIMIT_PUBLIC_PER_MINUTE", "")
	t.Setenv("RATE_LIMIT_AUTH_PER_MINUTE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RateLimit.PublicPerMinute != 10 {
		t.Errorf("PublicPerMinute = %d, mau 10", cfg.RateLimit.PublicPerMinute)
	}
	if cfg.RateLimit.AuthPerMinute != 5 {
		t.Errorf("AuthPerMinute = %d, mau 5", cfg.RateLimit.AuthPerMinute)
	}
	if cfg.Storage.AccessKey != "root-dev" || cfg.Storage.SecretKey != "rahasia-dev" {
		t.Errorf("fallback kredensial dev tidak dipakai: access=%q secret=%q",
			cfg.Storage.AccessKey, cfg.Storage.SecretKey)
	}
}

func TestLoad_MenolakDatabaseURLKosong(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("S3_ENDPOINT", "localhost:9000")

	if _, err := Load(); err == nil {
		t.Fatal("DATABASE_URL kosong harus ditolak")
	}
}

func TestLoad_MenolakEndpointStorageKosong(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://contoh")
	t.Setenv("S3_ENDPOINT", "")

	if _, err := Load(); err == nil {
		t.Fatal("S3_ENDPOINT kosong harus ditolak")
	}
}
