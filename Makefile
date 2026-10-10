BIN := bin/ndiskless
GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)

# Include the source commit in each binary. A plain go build produces "dev" and cannot be linked
# to a specific commit during incident triage. Preserve the --dirty suffix so builds from
# uncommitted working trees are distinguishable from clean builds.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build dev web gen migrate test test-web e2e verify fmt-check vet build-static verify-web verify-all deb offline

# Go embeds web/dist; share this prerequisite even under make -j.
build dev migrate test test-web vet build-static: web

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath $(LDFLAGS) -o $(BIN) ./cmd/ndiskless

dev:
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o bin/ndiskless-dev ./cmd/ndiskless

web:
	npm --prefix web ci
	npm --prefix web run build

gen:
	@echo "code generation is implemented in issue 002"

migrate:
	go run ./cmd/ndiskless -migrate-only

test:
	go test ./...

# Frontend behaviour tests (vitest + jsdom), using the same web build.
test-web:
	npm --prefix web run test

# End-to-end matrices against a deployed server. Needs ND_BASE/ND_PASSWORD and
# a real Windows source image; see tools/e2e/README.md. Never point it at a
# production pool — it creates and destroys its own images and groups.
e2e:
	python3 tools/e2e/config_reduction_matrix.py
	python3 tools/e2e/boot_matrix.py
	python3 tools/e2e/adaptation_matrix.py
	python3 tools/e2e/driver_matrix.py
	python3 tools/e2e/platform_matrix.py
	python3 tools/e2e/pool_matrix.py

# ---------------------------------------------------------------- Verification gates
#
# Use the same verify target locally and in CI so one cannot pass while the other fails.
# Any failed step fails the entire verification run.

fmt-check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$out" ]; then echo "Files needing gofmt:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

# Static binary distribution: one file, no CGO; an unsuccessful build blocks release.
build-static:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath $(LDFLAGS) -o bin/ndiskless-linux-amd64 ./cmd/ndiskless

# Debian package: install files, declare dependencies, and provide the configure command; do not start services.
# dpkg-deb is Linux-only; run this on Ubuntu or in CI, not macOS.
deb: build-static
	VERSION=$(VERSION) BINARY=bin/ndiskless-linux-amd64 packaging/build-deb.sh

# Offline: collect the package and dependencies into a tar.gz installable without network access.
# Run on an online Ubuntu host matching the target version because dependency packages are release-specific.
offline: deb
	DEB=$$(ls -t dist/ndiskless_*.deb | head -1) packaging/offline-bundle.sh

verify: fmt-check vet test build-static
	@echo "  Backend verification passed (gofmt / vet / go test / static build)"

verify-web: test-web
	@echo "  Frontend verification passed (vitest / vite build)"

verify-all: verify verify-web
