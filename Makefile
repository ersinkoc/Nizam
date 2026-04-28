VERSION ?= 0.1.0-dev
COMMIT ?= local
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
ACTIONLINT_VERSION ?= v1.7.12

.PHONY: dev ui binary docker docker-ssh container-scan workflow-lint test coverage lint vuln e2e deploy-drill production-doctor release-check clean

dev:
	go run ./cmd/mizan serve

ui:
	cd webui && npm install && npm run build
	rm -rf internal/server/dist
	cp -r webui/dist internal/server/dist

binary: ui
	go build -trimpath -ldflags="-s -w -X github.com/mizanproxy/mizan/internal/version.Version=$(VERSION) -X github.com/mizanproxy/mizan/internal/version.Commit=$(COMMIT) -X github.com/mizanproxy/mizan/internal/version.Date=$(DATE)" -o dist/mizan ./cmd/mizan

docker: ui
	docker build --target runtime --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t mizan:local .

docker-ssh: ui
	docker build --target runtime-ssh --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t mizan:ssh-local .

container-scan: docker docker-ssh
	docker scout cves --exit-code --only-severity critical,high local://mizan:local
	docker scout cves --exit-code --only-severity critical,high local://mizan:ssh-local

workflow-lint:
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

test:
	go test ./...
	cd webui && npm test

coverage:
	mkdir -p dist
	go test -coverprofile dist/coverage.out ./...
	go tool cover -func dist/coverage.out
	cd webui && npm run test:coverage

lint: workflow-lint
	go test ./...
	cd webui && npm run lint

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd webui && npm audit --audit-level=low

e2e:
	cd webui && npm run test:e2e

deploy-drill:
	mkdir -p dist
	go run ./cmd/mizan deploy drill --summary --out dist/staging-drill-summary.json
	go run ./cmd/mizan deploy drill verify --file dist/staging-drill-summary.json

production-doctor:
	mkdir -p dist
	rm -rf dist/doctor-home
	go run ./cmd/mizan doctor --home dist/doctor-home --production --json --out dist/production-doctor.json

release-check: workflow-lint coverage vuln e2e deploy-drill production-doctor binary container-scan

clean:
	rm -rf dist webui/dist internal/server/dist/assets
