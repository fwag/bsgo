#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$DIR/bin/bsgo"

echo "=== bsgo Fast Interdependent Projects Demo (Readline + Bash) ==="
echo "Upstream:   Base:System (readline)"
echo "Downstream: Shells (bash, depends on Base:System:standard)"
echo ""

# 1. Add Upstream Project & Package
echo "1. Adding Base:System project and readline package..."
"$BIN" project add "$DIR/test/fixtures/demo-fast/base-system-project.yaml"
"$BIN" package add "Base:System" "$DIR/test/fixtures/demo-fast/readline-package.yaml"

# 2. Add Downstream Project & Package (with target dependency)
echo ""
echo "2. Adding Shells project and bash package..."
"$BIN" project add "$DIR/test/fixtures/demo-fast/shells-project.yaml"
"$BIN" package add "Shells" "$DIR/test/fixtures/demo-fast/bash-package.yaml"

# 3. List registered projects and packages
echo ""
echo "3. Querying registered projects and dependencies:"
"$BIN" project list
echo ""
"$BIN" package list "Base:System"
"$BIN" package list "Shells"

# 4. Trigger Upstream Rebuild
echo ""
echo "4. Injecting upstream rebuild via admin tool..."
"$BIN" admin trigger Base:System readline \
  --priority high \
  --reason "READLINE_UPSTREAM_RELEASE" \
  --target standard \
  --arch x86_64

echo ""
echo "✔ Demo setup complete!"
echo "The system is now primed for rapid cross-project cascading:"
echo "  1. readline (Base:System) compiles on worker (~20-30s)"
echo "  2. bsgo-server signs repodata & emits bsgo.events.published"
echo "  3. Cascade Engine detects Shells/bash depends on Base:System"
echo "  4. bash is automatically triggered and compiled (~1-2m)!"
echo ""
echo "To monitor live builds, run in another terminal:"
echo "  $BIN watch"
