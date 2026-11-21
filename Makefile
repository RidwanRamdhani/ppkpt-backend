-include .env
export

# GNU Make di Windows tidak selalu menemukan sh.exe meski Git/MSYS terpasang,
# sehingga resep POSIX bisa gagal tanpa pesan. Pilih sh yang tersedia.
ifeq ($(OS),Windows_NT)
  SH_CANDIDATES := C:/msys64/usr/bin/sh.exe C:/Program Files/Git/usr/bin/sh.exe C:/Program Files/Git/bin/sh.exe
  SH_FOUND := $(firstword $(wildcard $(SH_CANDIDATES)))
  ifneq ($(SH_FOUND),)
    SHELL := $(SH_FOUND)
  endif
endif

.PHONY: up migrate-up migrate-down run test test-integration lint vuln

up:
	docker compose -f deploy/docker-compose.yml --env-file .env up -d db silo mailpit

migrate-up:
	migrate -path migrations -database "$(MIGRATE_URL)" up

migrate-down:
	migrate -path migrations -database "$(MIGRATE_URL)" down

run:
	$(GO) run ./cmd/api

test:
	$(GO) test ./... -race -cover -count=1

# Memerlukan database yang sudah dimigrasi dan .env berisi DATABASE_URL.
test-integration:
	TEST_DATABASE_URL="$(DATABASE_URL)" $(GO) test ./internal/audit ./internal/workflow -run TestIntegrasi -count=1

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...