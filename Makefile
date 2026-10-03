BINARY := termilink
PKG := ./cmd/termilink
GOFLAGS ?=

# Version stamped into internal/version.Version. Empty keeps the source default;
# releases inject the git tag via GoReleaser instead.
VERSION ?=
LDFLAGS := $(if $(VERSION),-X github.com/saliherden/termilink/internal/version.Version=$(VERSION),)

# macOS binds a Full Disk Access or Desktop-folder grant to the binary's code
# signature. The build's default is an ad-hoc signature whose hash changes on
# every rebuild, so the grant is lost the moment you rebuild. Signing with a
# stable local identity keeps the same signature across rebuilds and the grant
# with it. Run `make cert` to create the identity; other platforms ignore this
# and stay ad-hoc signed.
SIGN_IDENTITY ?= TermiLink Local
SIGN_IDENTIFIER ?= com.termilink.agent
SIGN_KEYCHAIN ?= $(HOME)/Library/Keychains/termilink-signing.keychain-db
SIGN_KEYCHAIN_PASSWORD ?= termilink

.PHONY: all build run test test-race vet fmt clean install lint sign cert cert-help \
	service-render service-install service-uninstall service-status

all: build

# @ keeps the recipe line itself off stdout, so `make build | ...` yields only
# the build's own output. The compiler errors still go to stderr.
build:
	@go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)
	@$(MAKE) --no-print-directory sign

# sign re-signs an already-built binary, and is what build calls. On macOS it
# signs with $(SIGN_IDENTITY) when that identity exists in the keychain and
# leaves the binary ad-hoc signed otherwise, with a note rather than an error:
# a machine without the certificate can still build and run the agent.
sign:
	@if [ -f "$(SIGN_KEYCHAIN)" ]; then security unlock-keychain -p "$(SIGN_KEYCHAIN_PASSWORD)" "$(SIGN_KEYCHAIN)" >/dev/null 2>&1 || true; fi
	@if [ "$$(uname -s)" = "Darwin" ] && command -v codesign >/dev/null 2>&1 && security find-identity -v -p codesigning 2>/dev/null | grep -qF "$(SIGN_IDENTITY)"; then \
		codesign --force --identifier "$(SIGN_IDENTIFIER)" --sign "$(SIGN_IDENTITY)" "$(BINARY)" && \
			echo "signed $(BINARY) with \"$(SIGN_IDENTITY)\""; \
	else \
		echo "note: $(BINARY) is ad-hoc signed; run 'make cert' for stable permissions"; \
	fi

# cert creates the local code-signing identity, non-interactively where possible.
# It may show one macOS authorization dialog for the trust step; the identity
# works for local signing even if that is declined. Idempotent.
cert:
	@SIGN_IDENTITY="$(SIGN_IDENTITY)" \
		SIGN_KEYCHAIN="$(SIGN_KEYCHAIN)" \
		SIGN_KEYCHAIN_PASSWORD="$(SIGN_KEYCHAIN_PASSWORD)" \
		./scripts/make-signing-cert.sh

# cert-help prints what `make cert` does, for anyone who would rather do it by
# hand in Keychain Access.
cert-help:
	@echo "Create a local code-signing identity (one time):"
	@echo "  make cert"
	@echo
	@echo "Or, by hand, with Keychain Access:"
	@echo "  Keychain Access > Certificate Assistant > Create a Certificate..."
	@echo "    Name: $(SIGN_IDENTITY)"
	@echo "    Identity Type: Self Signed Root"
	@echo "    Certificate Type: Code Signing"
	@echo "  Then open the certificate and set \"When using this certificate\" to Always Trust."
	@echo
	@echo "Verify you now have it:"
	@echo "  security find-identity -v -p codesigning | grep '$(SIGN_IDENTITY)'"
	@echo
	@echo "Then rebuild, reinstall, and re-grant Full Disk Access to the installed binary:"
	@echo "  make build && ./$(BINARY) service install"

install:
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(PKG)

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

# Service install. The binary is built and signed first because install copies
# it to ~/.local/bin/termilink, so a stale $(BINARY) would install last week's
# code with a signature the service no longer matches. $(SERVICE_CONFIG)
# defaults to the same path the binary uses on its own, ~/.termilink/config.yaml.
SERVICE_CONFIG ?= $(HOME)/.termilink/config.yaml
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