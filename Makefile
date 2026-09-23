BINARY := termilink
PKG := ./cmd/termilink
GOFLAGS ?=

.PHONY: all build run test vet fmt clean install lint

all: build

build:
	go build $(GOFLAGS) -o $(BINARY) $(PKG)

install:
	go install $(GOFLAGS) $(PKG)

run:
	go run $(GOFLAGS) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

lint: vet
	go test -run '^$$' -vet=all ./...

clean:
	rm -f $(BINARY)