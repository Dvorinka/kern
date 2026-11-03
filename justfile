# KERN task runner. Canonical commands live here, not in ad-hoc shell history.

set shell := ["bash", "-c"]

default:
	@just --list

build:
	go build ./...

test:
	go test ./... -count=1

vet:
	go vet ./...

# Run the control plane locally. Needs Postgres reachable at KERN_DATABASE_URL.
dev *env="":
	KERN_ENV=development \
	KERN_DATABASE_URL=${KERN_DATABASE_URL:-postgres://kern:kern@localhost:5432/kern?sslmode=disable} \
	KERN_BLOB_DIR=${KERN_BLOB_DIR:-.data/blobs} \
	KERN_RUNNER_JOIN_TOKEN=${KERN_RUNNER_JOIN_TOKEN:-kern-runner-join} \
	KERN_BOOTSTRAP_EMAIL=${KERN_BOOTSTRAP_EMAIL:-admin@kern.local} \
	KERN_BOOTSTRAP_PASSWORD=${KERN_BOOTSTRAP_PASSWORD:-kern-admin-password} \
	go run ./cmd/api

# Run a runner agent against the api. Extra args pass to the binary.
runner *args:
	KERN_ENV=development go run ./cmd/runner --work-root .data/runner {{args}}

migrate:
	KERN_DATABASE_URL=${KERN_DATABASE_URL:-postgres://kern:kern@localhost:5432/kern?sslmode=disable} \
	go run ./cmd/migrate

compose-up:
	docker compose -f deployments/docker-compose.yml up -d --build

compose-down:
	docker compose -f deployments/docker-compose.yml down

compose-logs:
	docker compose -f deployments/docker-compose.yml logs -f api
