#!/usr/bin/env bash
# Wrapper to convert SLFO to bsgo format and validate manifests
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BSGO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SLFO_REPO="${1:-/home/ta/Devel/packages/SLFO}"
BRANCH="${2:-slfo-1.2}"
OUTPUT_DIR="${3:-/home/ta/Devel/packages/SLFO-bsgo}"

echo "============================================================"
echo " Converting SLFO to bsgo Project Structure"
echo " Source Repo: ${SLFO_REPO}"
echo " Branch:      ${BRANCH}"
echo " Output Dir:  ${OUTPUT_DIR}"
echo "============================================================"

python3 "${SCRIPT_DIR}/convert-slfo.py" \
    --slfo-repo "${SLFO_REPO}" \
    --branch "${BRANCH}" \
    --output-dir "${OUTPUT_DIR}"

echo ""
echo "==> Running manifest package tests..."
cd "${BSGO_ROOT}"
go test ./pkg/manifest

echo ""
echo "Conversion complete and verified! Output directory: ${OUTPUT_DIR}"
