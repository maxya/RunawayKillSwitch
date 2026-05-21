.PHONY: build up down restart logs clean test test-unit test-integration test-coverage vet lint ci status reset

# Docker Compose targets

build:
	docker compose build --pull

up:
	docker compose up -d

up-logs:
	docker compose up --build

down:
	docker compose down

down-v:
	docker compose down -v

restart:
	docker compose restart proxy-engine

logs:
	docker compose logs -f proxy-engine

status:
	docker compose ps
	docker exec killswitch-db redis-cli ping
	curl -s http://localhost:8531/api/status | python3 -m json.tool

reset:
	curl -X POST http://localhost:8531/api/reset

# Go test targets (run inside the proxy-engine directory)

test: test-unit test-integration

test-unit:
	cd proxy-engine && go test ./core/... -v -race

test-integration:
	cd proxy-engine && go test -tags integration ./core/... -v -race -timeout 60s

test-coverage:
	cd proxy-engine && go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out

vet:
	cd proxy-engine && go vet ./...

lint:
	cd proxy-engine && golangci-lint run ./...

ci: vet lint test-unit

# Clean build artifacts

clean:
	docker compose down -v
	rm -f proxy-engine/coverage.out
