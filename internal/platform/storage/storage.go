// Package storage menyediakan klien object storage S3-compatible (Silo) untuk
// berkas bukti. Bucket selalu privat: tidak ada akses anonim dan tidak ada
// presigned URL permanen. Seluruh unduhan harus melewati backend agar otorisasi
// dan audit tetap terjadi di server.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound menandai objek yang tidak ada di bucket. Pemanggil dapat
// memetakannya menjadi 404 tanpa membocorkan detail dari Silo.
var ErrNotFound = errors.New("objek tidak ditemukan")

// Config adalah parameter koneksi ke Silo/S3.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Client membungkus klien minio-go dan nama bucket yang dipakai aplikasi.
type Client struct {
	cli    *minio.Client
	bucket string
}

// Object adalah objek yang sudah dibuka beserta metadata yang dibutuhkan saat
// streaming ke respons HTTP.
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// New membuat klien, lalu memastikan bucket tersedia. Bucket dibuat privat
// (tanpa policy) sehingga hanya dapat diakses dengan kredensial service account
// yang diberikan ke aplikasi.
func New(ctx context.Context, cfg Config) (*Client, error) {
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: buat klien: %w", err)
	}

	c := &Client{cli: cli, bucket: cfg.Bucket}
	if err := c.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// ensureBucket membuat bucket bila belum ada. Bila dua instance start bersamaan,
// pembuatan bucket oleh instance lain dianggap sukses setelah dicek ulang.
func (c *Client) ensureBucket(ctx context.Context) error {
	exists, err := c.cli.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("storage: cek bucket %q: %w", c.bucket, err)
	}
	if exists {
		return nil
	}

	if err := c.cli.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
		exists, checkErr := c.cli.BucketExists(ctx, c.bucket)
		if checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("storage: buat bucket %q: %w", c.bucket, err)
	}
	return nil
}

// Bucket mengembalikan nama bucket yang dipakai.
func (c *Client) Bucket() string {
	return c.bucket
}

// Put mengunggah objek dengan key dan tipe konten yang sudah ditentukan server.
// Ukuran harus diketahui agar minio-go tidak perlu buffering seluruh berkas.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if key == "" {
		return errors.New("storage: object key kosong")
	}
	if size < 0 {
		return errors.New("storage: ukuran objek tidak valid")
	}

	_, err := c.cli.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("storage: unggah %q: %w", key, err)
	}
	return nil
}

// Get membuka objek dan mengambil ukuran serta tipe konten dari metadata Silo.
// Pemanggil wajib menutup Object.Body.
func (c *Client) Get(ctx context.Context, key string) (*Object, error) {
	obj, err := c.cli.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapError(err)
	}

	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, mapError(err)
	}

	return &Object{
		Body:        obj,
		Size:        info.Size,
		ContentType: info.ContentType,
	}, nil
}

// Remove menghapus objek. Operasi ini idempoten: menghapus objek yang sudah
// tidak ada bukan merupakan kesalahan.
func (c *Client) Remove(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	if err := c.cli.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("storage: hapus %q: %w", key, err)
	}
	return nil
}

// mapError menerjemahkan error Silo yang relevan menjadi sentinel aplikasi.
func mapError(err error) error {
	if err == nil {
		return nil
	}

	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NoSuchObject", "NoSuchBucket":
		return ErrNotFound
	}
	return fmt.Errorf("storage: ambil objek: %w", err)
}
