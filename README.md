# ppkpt-backend

Backend untuk Platform Manajemen Pelaporan & Penanganan Kasus PPKPT.

## Prasyarat

- Go 1.26+ (dependensi `minio-go` mensyaratkan Go >= 1.26)
- Docker + Docker Compose
- `golang-migrate` CLI
- `golangci-lint`, `govulncheck`

## Menjalankan

```bash
make up
make migrate-up
make run
```

## Struktur

```
cmd/          entrypoint (api, provision, seed)
internal/     kode aplikasi per modul
migrations/   migrasi SQL golang-migrate
deploy/       Dockerfile, docker-compose, init DB
docs/         dokumentasi (api, decisions, erd)
```

## Dokumentasi

- `docs/BACKEND_IMPLEMENTATION_GUIDE.md` — panduan implementasi per phase.
- `docs/DECISIONS.md` — keputusan teknis dan deviasi dari SDD.
- `docs/PHASE4_IMPLEMENTATION.md` — rincian implementasi Phase 4 (layanan platform).
- `docs/PHASE5_IMPLEMENTATION.md` — rincian implementasi Phase 5 (autentikasi, sesi, otorisasi, provisioning akun).
- `docs/erd.png` — ERD database.

## Konfigurasi

Salin `.env.example` menjadi `.env` lalu sesuaikan.

Di dev, `S3_ACCESS_KEY`/`S3_SECRET_KEY` boleh dikosongkan; aplikasi memakai
`MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD` dari compose. Di produksi isi dengan
service account Silo yang hanya berhak pada bucket bukti.

## Layanan platform (Phase 4)

- `internal/audit` — audit log append-only; perubahan data menulis audit di
  transaksi yang sama.
- `internal/platform/storage` + `internal/evidence` — validasi tipe dari isi
  berkas, object key acak, bucket privat, unduhan selalu lewat backend.
- `internal/platform/mailer` + `internal/notification` — antrean email async
  dengan retry dan template notifikasi.
- `internal/platform/captcha` + `internal/platform/ratelimit` — verifikasi
  captcha server-side dan token bucket per IP.

## Autentikasi dan akun (Phase 5)

- `internal/auth` — login, sesi cookie, otorisasi role/permission, ganti
  password, dan pembuatan/penonaktifan akun.
- Route: `POST /api/auth/login`, `POST /api/auth/logout`,
  `PUT /api/auth/password`, `GET /api/auth/me`.
- Route fitur memakai `deps.Auth.Protect(auth.PermXxx, handler)`.
- Tidak ada halaman manajemen akun. Akun dikelola oleh sistem:

```bash
make seed                      # akun awal dari SEED_* di .env
make seed-demo                 # akun demo (non-production)
make provision ARGS="list"
make provision ARGS='create -name "Satgas A" -username satgas.a -email a@kampus.ac.id -role SATGAS'
make provision ARGS="reset-password satgas.a"
make provision ARGS="deactivate satgas.a"
```

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
