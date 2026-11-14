export GOWORK := off
.DEFAULT_GOAL := build

BINARY := bin/browser-platform
IMAGE ?= mutexai-browser-platform:dev

.PHONY: build run migrate test integration keycloak-integration check fmt lint image helm-check install deploy uninstall

install:
	./scripts/install-platform.sh

deploy: install

uninstall:
	./scripts/uninstall-platform.sh

build:
	go build -o $(BINARY) ./src/cmd

run: build
	$(BINARY)

migrate: build
	$(BINARY) migrate

test:
	go test ./... -race -count=1

integration:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL must point to a disposable PostgreSQL database"; exit 1)
	go test ./src/storage/... -race -count=1 -v

keycloak-integration:
	@test -n "$$TEST_KEYCLOAK_URL" -a -n "$$TEST_KEYCLOAK_CLIENT_ID" -a -n "$$TEST_KEYCLOAK_CLIENT_SECRET" || (echo "Set the three TEST_KEYCLOAK_* variables for a disposable integration account"; exit 1)
	go test ./src/internal/keycloak ./src/partners -run 'TestAdminAPI|TestProvisioner' -race -count=1 -v -timeout=2m

fmt:
	gofmt -w src

lint:
	golangci-lint run --timeout=5m

check:
	@test -z "$$(gofmt -l src)" || (gofmt -l src; exit 1)
	go vet ./...
	$(MAKE) lint test helm-check

image:
	docker build -t $(IMAGE) .

helm-check:
	python3 scripts/check-chart.py
