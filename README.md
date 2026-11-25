# ppkpt-backend

Backend untuk Platform Manajemen Pelaporan & Penanganan Kasus PPKPT.

## Prasyarat

- Go 1.26+ (dependensi `minio-go` mensyaratkan Go >= 1.26)
- Docker + Docker Compose
- `golang-migrate` CLI
- `golangci-lint`, `govulncheck`

## Menjalankan

```bash
cp .env.example .env   # lalu isi MFA_ENCRYPTION_KEY (lihat bagian 2FA di bawah)
make up
make migrate-up
make run
```

> `MFA_ENABLED=true` di `.env.example` mewajibkan `MFA_ENCRYPTION_KEY` terisi;
> aplikasi akan menolak start bila belum. Untuk cepat di dev tanpa 2FA, set
> `MFA_ENABLED=false` (produksi selalu dipaksa `true` oleh config).

## Struktur

```
cmd/          entrypoint (api, provision, seed)
internal/     kode aplikasi per modul
migrations/   migrasi SQL golang-migrate
deploy/       Dockerfile, docker-compose, init DB
```

## Konfigurasi

Salin `.env.example` menjadi `.env` lalu sesuaikan.

Di dev, `S3_ACCESS_KEY`/`S3_SECRET_KEY` boleh dikosongkan; aplikasi memakai
`MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD` dari compose. Di produksi isi dengan
service account Silo yang hanya berhak pada bucket bukti.

```bash
make seed                      # akun awal dari SEED_* di .env
make seed-demo                 # akun demo (non-production)
make provision ARGS="list"
make provision ARGS='create -name "Satgas A" -username satgas.a -email a@kampus.ac.id -role SATGAS'
make provision ARGS="reset-password satgas.a"
make provision ARGS="reset-2fa --username satgas.a"
make provision ARGS="deactivate satgas.a"
```

## 2FA petugas

Login petugas memakai dua tahap: password lalu kode TOTP dari aplikasi
authenticator. Enrollment wajib pada login pertama. Endpoint yang dipakai
frontend: `POST /api/auth/login` → `POST /api/auth/2fa/setup` (hanya saat
`enrollment_required=true`) → `POST /api/auth/2fa/enable`, atau
`POST /api/auth/2fa/verify` pada login berikutnya. Recovery code ditampilkan
sekali saat enrollment dan dapat diterbitkan ulang lewat
`POST /api/auth/2fa/recovery`.

Aktifkan dengan `MFA_ENABLED=true` dan isi `MFA_ENCRYPTION_KEY` (32 byte
base64, mis. `openssl rand -base64 32`). Kunci yang sama harus dipakai terus;
menggantinya membuat seluruh secret TOTP lama tidak dapat dibaca. Di dev/test
2FA boleh dimatikan dengan `MFA_ENABLED=false` (default dev), tetapi produksi
mewajibkannya aktif.

Bila petugas kehilangan perangkat, jalankan
`make provision ARGS="reset-2fa --username satgas.a"`; petugas akan mendaftarkan
authenticator baru saat login berikutnya.

## Test

```bash
make test               # unit test; tes integrasi dilewati tanpa database
make test-integration   # termasuk tes autentikasi ke PostgreSQL (butuh migrasi)
make lint               # golangci-lint
make test-e2e           # script curl end-to-end terhadap API sungguhan
make postman-run        # koleksi Postman lewat Newman
```

Panduan lengkap (termasuk cara memakai Postman): `docs/TESTING_AUTH.md`.

Jika `go` di PATH masih versi lebih lama daripada yang diminta `go.mod` dan
`go test -cover` gagal dengan pesan `compile: version ... does not match go tool
version ...`, pasang Go sesuai `go.mod` dan pastikan `C:\Program Files\Go\bin`
berada di PATH.

Test integrasi audit (append-only) membutuhkan database yang sudah dimigrasi:

```bash
make test-integration
# atau:
TEST_DATABASE_URL="postgres://ppkpt_app:app_secret@localhost:5432/ppkpt?sslmode=disable" go test ./internal/audit
```
