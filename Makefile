-include .env
export

# Perintah Go dapat dioverride: make test GO="C:/Go/bin/go.exe"
GO ?= go

# GNU Make di Windows tidak selalu menemukan sh.exe meski Git/MSYS terpasang,
# sehingga resep POSIX bisa gagal tanpa pesan. Pilih sh yang tersedia.
ifeq ($(OS),Windows_NT)
  SH_CANDIDATES := C:/msys64/usr/bin/sh.exe C:/Program Files/Git/usr/bin/sh.exe C:/Program Files/Git/bin/sh.exe
  SH_FOUND := $(firstword $(wildcard $(SH_CANDIDATES)))
  ifneq ($(SH_FOUND),)
    SHELL := $(SH_FOUND)
  endif
endif

.PHONY: up migrate-up migrate-down run provision seed seed-demo test test-integration test-e2e postman-env postman-run lint vuln

up:
	docker compose -f deploy/docker-compose.yml --env-file .env up -d db silo mailpit

migrate-up:
	migrate -path migrations -database "$(MIGRATE_URL)" up

migrate-down:
	migrate -path migrations -database "$(MIGRATE_URL)" down

run:
	$(GO) run ./cmd/api

# Kelola akun petugas/pejabat, mis.: make provision ARGS="list"
#   make provision ARGS='create -name "Satgas A" -username satgas.a -email a@kampus.ac.id -role SATGAS'
provision:
	$(GO) run ./cmd/provision $(ARGS)

# Akun awal dari SEED_* di .env (aman dijalankan ulang).
seed:
	$(GO) run ./cmd/seed

# Tambah akun demo (Satgas A/B/C, Pejabat A). Ditolak bila APP_ENV=production.
seed-demo:
	$(GO) run ./cmd/seed -demo

test:
	$(GO) test ./... -race -cover -count=1

# Memerlukan database yang sudah dimigrasi dan .env berisi DATABASE_URL.
test-integration:
	TEST_DATABASE_URL="$(DATABASE_URL)" $(GO) test ./internal/audit ./internal/auth ./internal/cases ./internal/referral ./internal/report ./internal/review ./internal/workflow -count=1

# Uji end-to-end modul autentikasi terhadap API sungguhan (curl). Butuh database
# yang sudah dimigrasi; API dijalankan otomatis bila belum berjalan.
test-e2e:
	bash scripts/test-auth.sh

# Postman: buat akun uji + environment, lalu (opsional) jalankan lewat Newman.
# API harus berjalan dengan RATE_LIMIT_AUTH_PER_MINUTE=1000.
postman-env:
	bash scripts/prepare-postman.sh

postman-run: postman-env
	newman run postman/ppkpt-auth.postman_collection.json -e postman/ppkpt-local.generated.postman_environment.json

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...