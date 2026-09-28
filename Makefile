BINARY := termilink
PKG := ./cmd/termilink
GOFLAGS ?=

.PHONY: all build run test test-race vet fmt clean install lint

all: build

build:
	go build $(GOFLAGS) -o $(BINARY) $(PKG)

install:
	go install $(GOFLAGS) $(PKG)

run:
	go run $(GOFLAGS) $(PKG)

test:
	go test ./...

# What CI runs. Slower than `make test`, and it is the only way the agent
# relay's shared-timestamp race shows up locally.
test-race:
	go test -race -timeout 15m ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

lint: vet
	go test -run '^$$' -vet=all ./...

clean:
	rm -f $(BINARY)