package config

import "testing"

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
