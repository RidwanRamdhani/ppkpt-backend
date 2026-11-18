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

## Konfigurasi

Salin `.env.example` menjadi `.env` lalu sesuaikan.