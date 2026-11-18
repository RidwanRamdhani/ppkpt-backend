// Provide koneksi dan helper transaksi PostgreSQL (pgxpool).
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX adalah subset pgx yang dipenuhi baik oleh *pgxpool.Pool maupun pgx.Tx,
// sehingga repository dapat berjalan di luar maupun di dalam transaksi.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MustConnect membuka pool dan memverifikasi koneksi. Bila gagal, panic dengan
// pesan yang jelas agar proses tidak berjalan dalam kondisi setengah rusak.
func MustConnect(ctx context.Context, databaseURL string) *pgxpool.Pool {
	pool, err := Connect(ctx, databaseURL)
	if err != nil {
		panic(fmt.Sprintf("db: tidak dapat terhubung ke database: %v", err))
	}
	return pool
}

// Connect membuka pool dan memverifikasi koneksi dengan ping.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("buat pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// WithTx menjalankan fn dalam satu transaksi. Rollback ditunda sehingga aman
// dipanggil setelah Commit, karena pgx mengabaikannya bila transaksi sudah selesai.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
