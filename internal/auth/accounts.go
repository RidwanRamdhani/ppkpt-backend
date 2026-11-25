package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Pembuatan dan penonaktifan akun dilakukan oleh sistem (cmd/provision dan
// cmd/seed), bukan melalui halaman antarmuka. Setiap aksi tercatat di audit
// log dengan aktor SYSTEM.

var (
	// ErrUserExists dikembalikan CreateUser bila username atau email sudah dipakai.
	ErrUserExists = httpx.ErrConflict("Akun dengan username atau email tersebut sudah ada.")
	// ErrUserNotFound dikembalikan bila akun yang dituju tidak ada.
	ErrUserNotFound = httpx.NewAppError(404, httpx.CodeNotFound, "Akun tidak ditemukan.")
)

// usernamePattern: huruf kecil, angka, titik, garis bawah, dan strip; tanpa '@'
// agar username tidak pernah bentrok dengan email saat login.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,49}$`)

// CreateUserInput adalah masukan CreateUser.
type CreateUserInput struct {
	Name     string
	Username string
	Email    string
	Role     string
	Password string
	// MustChangePassword memaksa penggantian password saat login pertama
	// (FR-20). Nilai false hanya boleh dipakai untuk akun demo di lingkungan dev.
	MustChangePassword bool
	// Source menandai asal pembuatan di audit log, mis. "provision" atau "seed".
	Source string
}

// CreateUser membuat akun petugas/pejabat. Username dan email disimpan dalam
// huruf kecil.
func (s *Service) CreateUser(ctx context.Context, in CreateUserInput) (*User, error) {
	name := strings.TrimSpace(in.Name)
	username := strings.ToLower(strings.TrimSpace(in.Username))
	email := strings.ToLower(strings.TrimSpace(in.Email))

	fields := map[string]string{}
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		fields["name"] = "Nama wajib diisi (maksimal 100 karakter)."
	}
	if !usernamePattern.MatchString(username) {
		fields["username"] = "Username 3-50 karakter: huruf kecil, angka, titik, garis bawah, atau strip."
	}
	if !validEmail(email) {
		fields["email"] = "Email tidak valid (maksimal 120 karakter)."
	}
	if !IsValidRole(in.Role) {
		fields["role"] = "Role harus SATGAS atau PEJABAT."
	}
	if perr := ValidatePassword(in.Password, username, email, emailLocalPart(email)); perr != nil {
		fields["password"] = perr.Fields["password"]
	}
	if len(fields) > 0 {
		e := httpx.ErrValidation("Data akun tidak valid.")
		e.Fields = fields
		return nil, e
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}

	var u User
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = $1 OR lower(email) = $2)`,
			username, email).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrUserExists
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (name, username, email, password_hash, role, must_change_password)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, created_at`,
			name, username, email, hash, in.Role, in.MustChangePassword).Scan(&u.ID, &u.CreatedAt); err != nil {
			return err
		}
		u.Name, u.Username, u.Email, u.Role = name, username, email, in.Role
		u.IsActive, u.MustChangePassword = true, in.MustChangePassword
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorSystem, Action: audit.ActionUserCreated,
			Entity: "user", EntityID: userIDPtr(u.ID),
			Detail: map[string]any{"role": in.Role, "source": in.Source, "must_change_password": in.MustChangePassword},
		})
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // balapan antar proses
			return nil, ErrUserExists
		}
		return nil, err
	}
	return &u, nil
}

// DeactivateUser menonaktifkan akun dan mencabut seluruh sesinya. Mengembalikan
// false bila akun sudah nonaktif (idempoten).
func (s *Service) DeactivateUser(ctx context.Context, identifier, source string) (changed bool, err error) {
	ident := normalizeIdentifier(identifier)
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := findByIdentifier(ctx, tx, ident)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if !u.IsActive {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET is_active = FALSE WHERE id = $1`, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		changed = true
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorSystem, Action: audit.ActionUserDeactivated,
			Entity: "user", EntityID: userIDPtr(u.ID), Detail: map[string]any{"source": source},
		})
	})
	return changed, err
}

// ResetPassword mengganti password akun oleh sistem (mis. lupa password; tidak
// ada fitur lupa password di antarmuka). Akun diwajibkan mengganti password saat
// login berikutnya, kunci akun dibuka, dan seluruh sesi dicabut.
func (s *Service) ResetPassword(ctx context.Context, identifier, newPassword, source string) error {
	ident := normalizeIdentifier(identifier)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := findByIdentifier(ctx, tx, ident)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if perr := ValidatePassword(newPassword, u.Username, u.Email, emailLocalPart(u.Email)); perr != nil {
			return perr
		}
		hash, err := s.hasher.Hash(newPassword)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users SET password_hash = $2, must_change_password = TRUE,
				failed_login_count = 0, locked_until = NULL WHERE id = $1`, u.ID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorSystem, Action: audit.ActionPasswordChanged,
			Entity: "user", EntityID: userIDPtr(u.ID), Detail: map[string]any{"by": "reset", "source": source},
		})
	})
	return err
}

// ListUsers mengembalikan seluruh akun (tanpa data rahasia), urut berdasarkan ID.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, username, email, role, is_active, must_change_password, last_login_at, created_at,
			totp_enabled_at IS NOT NULL
		FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Username, &u.Email, &u.Role, &u.IsActive,
			&u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt, &u.TotpEnabled); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ResetMFA menghapus perangkat 2FA seorang petugas (dipakai bila perangkat
// hilang). Secret, recovery code, challenge yang belum terpakai, dan seluruh
// sesi dicabut agar petugas wajib enroll ulang pada login berikutnya.
func (s *Service) ResetMFA(ctx context.Context, identifier, source string) error {
	ident := normalizeIdentifier(identifier)
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := findByIdentifier(ctx, tx, ident)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users SET totp_secret_enc = NULL, totp_enabled_at = NULL, totp_last_step = NULL
			WHERE id = $1`, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorSystem, UserID: userIDPtr(u.ID), Action: audit.ActionMFAResetByAdmin,
			Entity: "user", EntityID: userIDPtr(u.ID), Detail: map[string]any{"source": source},
		})
	})
}

func validEmail(email string) bool {
	if email == "" || len(email) > 120 {
		return false
	}
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && strings.Contains(email[strings.IndexByte(email, '@'):], ".")
}
