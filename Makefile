-include .env
export

.PHONY: up migrate-up migrate-down run test lint vuln

up:
	docker compose -f deploy/docker-compose.yml --env-file .env up -d db silo mailpit

migrate-up:
	migrate -path migrations -database "$(MIGRATE_URL)" up

migrate-down:
	migrate -path migrations -database "$(MIGRATE_URL)" down

run:
	go run ./cmd/api

test:
	go test ./... -race -cover

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...