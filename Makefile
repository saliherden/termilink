BINARY := termilink
PKG := ./cmd/termilink
GOFLAGS ?=

.PHONY: all build run test test-race vet fmt clean install lint \
	service-render service-install service-uninstall service-status

all: build

# @ keeps the recipe line itself off stdout, so `make build | ...` yields only
# the build's own output. The compiler errors still go to stderr.
build:
	@go build $(GOFLAGS) -o $(BINARY) $(PKG)

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

# Service install. The binary is built first because the generated definition
# points at the binary that ran the command, so a stale $(BINARY) would install
# a job running last week's code.
SERVICE_CONFIG ?= config.yaml
# install and uninstall both ask before acting, which make cannot answer for
# you. SERVICE_ARGS carries the answer: make service-install SERVICE_ARGS=-y
SERVICE_ARGS ?=

# @ silences the echoed recipe lines, so the plist on stdout is the only
# output. That is what makes this pipeable:
#   make service-render | plutil -lint -
service-render: build
	@./$(BINARY) --config $(SERVICE_CONFIG) service render $(SERVICE_ARGS)

service-install: build
	@./$(BINARY) --config $(SERVICE_CONFIG) service install $(SERVICE_ARGS)

service-uninstall: build
	@./$(BINARY) --config $(SERVICE_CONFIG) service uninstall $(SERVICE_ARGS)

service-status: build
	@./$(BINARY) --config $(SERVICE_CONFIG) service status $(SERVICE_ARGS)