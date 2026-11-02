-include .env
export

.PHONY: dev up down run build test vet smoke migrate-status migrate-down new-migration

up:            ## start Postgres and Redis
	docker compose up -d postgres redis

down:
	docker compose down

run:           ## run the API locally (migrates on start)
	go run ./cmd/api

dev: up run

build:
	CGO_ENABLED=0 go build -o bin/api ./cmd/api

test:
	go test ./...

vet:
	go vet ./...

smoke:         ## end-to-end test against a running API
	./scripts/smoke.sh

# goose CLI: go install github.com/pressly/goose/v3/cmd/goose@latest
migrate-status:
	goose -dir migrations postgres "$(DATABASE_URL)" status

migrate-down:
	goose -dir migrations postgres "$(DATABASE_URL)" down

new-migration: ## make new-migration name=add_invoices
	goose -dir migrations create $(name) sql
