#!/bin/sh
# Create a stable local code-signing identity for TermiLink on macOS.
#
# Why: macOS binds a Full Disk Access or Desktop-folder grant to the binary's
# code signature. `make build` produces an ad-hoc signature whose hash changes on
# every rebuild, so a grant is lost the next time you build. A self-signed
# certificate gives the binary a stable identity, and the grant survives.
#
# The certificate lives in its own keychain under ~/Library/Keychains and never
# leaves the machine. This script is idempotent: it exits quietly if the identity
# already exists.
#
# Usage: scripts/make-signing-cert.sh  (or `make cert`)

set -eu

IDENTITY="${SIGN_IDENTITY:-TermiLink Local}"
KEYCHAIN="${SIGN_KEYCHAIN:-$HOME/Library/Keychains/termilink-signing.keychain-db}"
KEYCHAIN_PASSWORD="${SIGN_KEYCHAIN_PASSWORD:-termilink}"
DAYS=3650

if [ "$(uname -s)" != "Darwin" ]; then
	echo "note: code signing is a macOS concern; nothing to do on $(uname -s)" >&2
	exit 0
fi

if security find-identity -v -p codesigning 2>/dev/null | grep -qF "$IDENTITY"; then
	echo "identity \"$IDENTITY\" already exists; nothing to do"
	exit 0
fi

if ! command -v openssl >/dev/null 2>&1; then
	echo "error: openssl is required to create the certificate" >&2
	exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "generating a self-signed code-signing certificate \"$IDENTITY\"..."

# extendedKeyUsage=codeSigning is what makes codesign accept the certificate as a
# signing identity rather than rejecting it as the wrong purpose.
openssl req -x509 -newkey rsa:2048 -sha256 -days "$DAYS" -nodes \
	-keyout "$tmp/key.pem" -out "$tmp/cert.pem" \
	-subj "/CN=$IDENTITY/O=TermiLink" \
	-addext "keyUsage=critical,digitalSignature" \
	-addext "extendedKeyUsage=critical,codeSigning" \
	-addext "basicConstraints=critical,CA:false" >/dev/null 2>&1

# -legacy uses RC2/3DES, which macOS's Security framework can verify; OpenSSL 3's
# default AES/PBKDF2 encoding is rejected with "MAC verification failed". Apple's
# LibreSSL does not know -legacy, so fall back to a plain export there. The
# password must be non-empty: macOS rejects an empty-password PKCS#12 too.
if ! openssl pkcs12 -export -legacy -inkey "$tmp/key.pem" -in "$tmp/cert.pem" \
	-name "$IDENTITY" -out "$tmp/id.p12" -passout pass:"$KEYCHAIN_PASSWORD" >/dev/null 2>&1; then
	openssl pkcs12 -export -inkey "$tmp/key.pem" -in "$tmp/cert.pem" \
		-name "$IDENTITY" -out "$tmp/id.p12" -passout pass:"$KEYCHAIN_PASSWORD" >/dev/null 2>&1
fi

if [ ! -f "$KEYCHAIN" ]; then
	echo "creating keychain $KEYCHAIN"
	security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"
fi
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"

echo "importing the identity..."
security import "$tmp/id.p12" -k "$KEYCHAIN" -P "$KEYCHAIN_PASSWORD" \
	-T /usr/bin/codesign -T /usr/bin/security -T /usr/bin/productsign

# Without this the key's ACL still prompts on every codesign; the password is the
# keychain's own, which this script just set.
security set-key-partition-list -S apple-tool:,apple:,codesign: -s \
	-k "$KEYCHAIN_PASSWORD" "$KEYCHAIN" >/dev/null 2>&1 || true

# Put the keychain on the user's search list so codesign finds the identity.
security list-keychains -d user -s "$KEYCHAIN" \
	$(security list-keychains -d user | sed -e 's/^[[:space:]]*//' -e 's/"//g')

# Trust the certificate for code signing. This may show a one-time authorization
# dialog; if it is refused, the identity still exists and codesign still signs,
# which is what TCC matches on.
if ! security add-trusted-cert -p codeSign -r trustRoot -k "$KEYCHAIN" "$tmp/cert.pem"; then
	echo "warning: could not set trust automatically; the identity still works for local signing" >&2
fi

echo
echo "code-signing identities now available:"
security find-identity -v -p codesigning

if security find-identity -v -p codesigning 2>/dev/null | grep -qF "$IDENTITY"; then
	echo
	echo "done. \"$IDENTITY\" is ready; run 'make build' to sign the binary."
else
	echo
	echo "error: \"$IDENTITY\" was not created" >&2
	exit 1
fi
