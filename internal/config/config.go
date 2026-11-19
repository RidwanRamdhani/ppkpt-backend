package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppEnv      string
	HTTPAddr    string
	AppOrigin   string
	DatabaseURL string

	CookieSecure bool

	SessionIdleMinutes   int
	SessionAbsoluteHours int

	BcryptCost int

	LoginMaxAttempts int
	LoginLockMinutes int
	PinMaxAttempts   int
	PinLockMinutes   int

	Storage Storage

	MaxUploadBytes     int64
	MaxFilesPerRequest int

	CaptchaEnabled  bool
	CaptchaProvider string
	CaptchaSecret   string

	SMTP SMTP

	TrustedProxyCIDRs []string

	Deadline  Deadline
	RateLimit RateLimit
}

type Storage struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

type SMTP struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

type Deadline struct {
	ReviewDays     int
	ExamDays       int
	WarnReviewDays int
	WarnExamDays   int
}

// RateLimit mengatur kuota permintaan per IP per menit. Endpoint publik
// (pelaporan, cek status) dibuat lebih ketat daripada endpoint autentikasi.
type RateLimit struct {
	PublicPerMinute int
	AuthPerMinute   int
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:               getenv("APP_ENV", "dev"),
		HTTPAddr:             getenv("HTTP_ADDR", ":8080"),
		AppOrigin:            getenv("APP_ORIGIN", "http://localhost:3000"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		CookieSecure:         getenvBool("COOKIE_SECURE", false),
		SessionIdleMinutes:   getenvInt("SESSION_IDLE_MINUTES", 30),
		SessionAbsoluteHours: getenvInt("SESSION_ABSOLUTE_HOURS", 8),
		BcryptCost:           getenvInt("BCRYPT_COST", 12),
		LoginMaxAttempts:     getenvInt("LOGIN_MAX_ATTEMPTS", 5),
		LoginLockMinutes:     getenvInt("LOGIN_LOCK_MINUTES", 15),
		PinMaxAttempts:       getenvInt("PIN_MAX_ATTEMPTS", 5),
		PinLockMinutes:       getenvInt("PIN_LOCK_MINUTES", 15),
		Storage: Storage{
			Endpoint:  os.Getenv("S3_ENDPOINT"),
			AccessKey: storageAccessKey(),
			SecretKey: storageSecretKey(),
			Bucket:    getenv("S3_BUCKET", "ppkpt-evidence"),
			UseSSL:    getenvBool("S3_USE_SSL", false),
		},
		MaxUploadBytes:     int64(getenvInt("MAX_UPLOAD_BYTES", 10485760)),
		MaxFilesPerRequest: getenvInt("MAX_FILES_PER_REQUEST", 10),
		CaptchaEnabled:     getenvBool("CAPTCHA_ENABLED", false),
		CaptchaProvider:    getenv("CAPTCHA_PROVIDER", "turnstile"),
		CaptchaSecret:      os.Getenv("CAPTCHA_SECRET"),
		SMTP: SMTP{
			Host: getenv("SMTP_HOST", "localhost"),
			Port: getenvInt("SMTP_PORT", 1025),
			User: os.Getenv("SMTP_USER"),
			Pass: os.Getenv("SMTP_PASS"),
			From: getenv("SMTP_FROM", "noreply@ppkpt.local"),
		},
		Deadline: Deadline{
			ReviewDays:     getenvInt("DEADLINE_REVIEW_DAYS", 7),
			ExamDays:       getenvInt("DEADLINE_EXAM_DAYS", 30),
			WarnReviewDays: getenvInt("DEADLINE_WARN_REVIEW_DAYS", 2),
			WarnExamDays:   getenvInt("DEADLINE_WARN_EXAM_DAYS", 7),
		},
		RateLimit: RateLimit{
			PublicPerMinute: getenvInt("RATE_LIMIT_PUBLIC_PER_MINUTE", 10),
			AuthPerMinute:   getenvInt("RATE_LIMIT_AUTH_PER_MINUTE", 5),
		},
		TrustedProxyCIDRs: splitCSV(os.Getenv("TRUSTED_PROXY_CIDRS")),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL wajib diisi")
	}
	if cfg.Storage.Endpoint == "" {
		return Config{}, fmt.Errorf("S3_ENDPOINT wajib diisi")
	}
	if cfg.Storage.Bucket == "" {
		return Config{}, fmt.Errorf("S3_BUCKET wajib diisi")
	}

	return cfg, nil
}

func MustLoad() Config {
	cfg, err := Load()
	if err != nil {
		panic(err)
	}
	return cfg
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// storageAccessKey mengembalikan kredensial service account aplikasi. Di dev,
// bila S3_ACCESS_KEY belum diisi, kredensial root Silo dari compose dipakai agar
// `make up && make run` langsung berjalan. Produksi wajib memakai service
// account terbatas (hanya bucket bukti), bukan kredensial root.
func storageAccessKey() string {
	if v := os.Getenv("S3_ACCESS_KEY"); v != "" {
		return v
	}
	return os.Getenv("MINIO_ROOT_USER")
}

// storageSecretKey adalah pasangan storageAccessKey untuk lingkungan dev.
func storageSecretKey() string {
	if v := os.Getenv("S3_SECRET_KEY"); v != "" {
		return v
	}
	return os.Getenv("MINIO_ROOT_PASSWORD")
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
