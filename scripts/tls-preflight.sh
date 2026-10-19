#!/bin/sh
# Read-only preflight for a planned AgentChat TLS hub. Does not install trust.
set -eu

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <cert.pem> <key.pem> <ca.pem> <https-url>" >&2
  exit 2
fi

CERT=$1
KEY=$2
CA=$3
URL=$4

for f in "$CERT" "$KEY" "$CA"; do
  [ -r "$f" ] || { echo "not readable: $f" >&2; exit 1; }
done

echo "== certificate =="
openssl x509 -in "$CERT" -noout -subject -issuer -dates -ext subjectAltName
openssl x509 -in "$CERT" -checkend 86400 -noout >/dev/null || {
  echo "certificate expires within 24 hours" >&2
  exit 1
}

echo "== key match =="
CERT_MOD=$(openssl x509 -in "$CERT" -noout -modulus | openssl sha256)
KEY_MOD=$(openssl rsa -in "$KEY" -noout -modulus 2>/dev/null | openssl sha256)
[ "$CERT_MOD" = "$KEY_MOD" ] || {
  echo "certificate and key do not match" >&2
  exit 1
}
echo "match"

echo "== chain =="
openssl verify -CAfile "$CA" "$CERT"

echo "== health over TLS =="
curl --fail --silent --show-error --cacert "$CA" "$URL/healthz"
echo

echo "== fingerprint =="
openssl x509 -in "$CERT" -outform der | sha256sum | awk '{print "sha256:" $1}'
