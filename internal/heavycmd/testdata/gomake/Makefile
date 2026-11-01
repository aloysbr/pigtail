# usage: make check — runs: everything
VERSION := 1.0
LDFLAGS := -X main.Version=$(VERSION)
export GOFLAGS = -mod=mod
URL = http://x

ifeq ($(GOOS),linux)
EXTRA = linux
else
EXTRA = other
endif

define HELP
x: go test -race ./...
endef

.PHONY: all check \
	vet test-race

all: test-race

check: vet test-race web
	@echo ok

vet:
	go vet ./...

test-race:
	go test -race -count=1 ./...

test:
	go test ./...

unit:
	go test ./internal/foo/

web:
	cd web && npm ci && npm run build

vuln:
	"$(GOVULNCHECK)" ./...

loop: loop2
loop2: loop

build:
	go build ./...

split:
	go test \
	./...

wrapped: test-race admitted

admitted:
	pigtail admit --class go-test -- go test -race ./...

cond:
ifdef CI
	go test -race ./...
else
	go vet ./...
endif

8:
	go test -race ./...

inlinenote: ; go test -race ./... # note
inlinevet: ; go vet ./... # go test -race ./...
inlinehash: ; echo a#b && go test -race ./...

stamped:
	@sh scripts/stamp.sh run -- $(MAKE) --no-print-directory test-race
stampedvet:
	@sh scripts/stamp.sh run -- ${MAKE} vet
