GOTOOLCHAIN ?= go$(shell awk '$$1 == "go" { print $$2 }' go.mod)
export GOTOOLCHAIN

TIME_LIMIT ?= time-limit
LINUX_IMAGE ?= golang:1.27.1-bookworm
VERSION = $(shell awk '/^\#\# \[/ {gsub(/[][]/, "", $$2); print $$2; exit}' CHANGELOG.md)

.PHONY: build install install-requirements format tidy lint test test-run test-build test-linux release-assets release

build:
	mkdir -p bin
	go build -ldflags '-X main.buildVersion=v$(VERSION)' -o bin/time-limit .

install:
	go install .

install-requirements:
	go mod download

format:
	gofmt -w .

tidy:
	go mod tidy

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"
	commentcensor .
	go mod tidy -diff

test:
	$(TIME_LIMIT) 560 $(MAKE) --no-print-directory test-run

test-run: build
	TIME_LIMIT_TEST_BINARY="$(CURDIR)/bin/time-limit" go test -count=1 -timeout=5m $(if $(TEST),-run '$(TEST)',) ./...

test-build:
	$(TIME_LIMIT) 560 go test -run '^$$' ./...

test-linux:
	$(TIME_LIMIT) 560 docker run --rm --init -v "$(CURDIR):/src" -w /src -e CGO_ENABLED=0 $(LINUX_IMAGE) timeout --kill-after=10s 530 make test-run

release-assets:
	mkdir -p bin/release
	for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do \
		platform=$${target%/*}; architecture=$${target#*/}; \
		CGO_ENABLED=0 GOOS=$$platform GOARCH=$$architecture go build -trimpath -ldflags '-s -w -X main.buildVersion=v$(VERSION)' -o bin/release/time-limit-$$platform-$$architecture . || exit; \
	done
	cd bin/release && shasum -a 256 time-limit-* > SHA256SUMS

release: lint test release-assets
	test -z "$$(git status --porcelain)"
	git fetch origin main
	test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)"
	git tag -a v$(VERSION) -m v$(VERSION)
	git push origin v$(VERSION)
	gh release create v$(VERSION) bin/release/* --title v$(VERSION) --notes-file CHANGELOG.md
