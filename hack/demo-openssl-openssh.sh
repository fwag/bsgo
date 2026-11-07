#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$DIR/bin/bsgo"

echo "=== bsgo End-to-End Interdependent Projects Demo ==="
echo "Upstream:   openSUSE:Crypto (openssl)"
echo "Downstream: openSUSE:Network (openssh, depends on openSUSE:Crypto:standard)"
echo ""

# 1. Add Upstream Project & Package
echo "1. Adding openSUSE:Crypto project and openssl package..."
"$BIN" project add "$DIR/test/fixtures/demo/crypto-project.yaml"
"$BIN" package add "openSUSE:Crypto" "$DIR/test/fixtures/demo/openssl-package.yaml"

# 2. Add Downstream Project & Package (with target dependency)
echo ""
echo "2. Adding openSUSE:Network project and openssh package..."
"$BIN" project add "$DIR/test/fixtures/demo/network-project.yaml"
"$BIN" package add "openSUSE:Network" "$DIR/test/fixtures/demo/openssh-package.yaml"

# 3. List registered projects and packages
echo ""
echo "3. Querying registered projects and dependencies:"
"$BIN" project list
echo ""
"$BIN" package list "openSUSE:Crypto"
"$BIN" package list "openSUSE:Network"

# 4. Trigger High-Priority Upstream Security Rebuild
echo ""
echo "4. Injecting high-priority upstream security rebuild via admin tool..."
"$BIN" admin trigger openSUSE:Crypto openssl \
  --priority high \
  --reason "CRITICAL_CVE_REBUILD" \
  --target standard \
  --arch x86_64

echo ""
echo "✔ Demo setup complete!"
echo "The system is now primed for cross-project cascading:"
echo "  1. openssl (openSUSE:Crypto) builds on worker"
echo "  2. bsgo-server signs repodata & emits bsgo.events.published"
echo "  3. Cascade Engine detects openSUSE:Network/openssh depends on openSUSE:Crypto"
echo "  4. openssh is automatically triggered and compiled!"
echo ""
echo "To trigger openssh directly instead of waiting for cascade, run:"
echo "  $BIN admin trigger openSUSE:Network openssh --priority high"
echo ""
echo "To monitor live builds, run in another terminal:"
echo "  $BIN watch"
