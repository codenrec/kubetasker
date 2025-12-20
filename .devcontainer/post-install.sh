#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="$HOME/.local/bin"
mkdir -p "$BIN_DIR"

echo "▶ Installing project-specific tooling..."

ARCH="$(uname -m)"
OS="$(uname | tr '[:upper:]' '[:lower:]')"

case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

### ---- kind -------------------------------------------------
KIND_VERSION=v0.23.0

if ! command -v kind >/dev/null 2>&1; then
  echo "▶ Installing kind ${KIND_VERSION}"
  curl -Lo "${BIN_DIR}/kind" \
    "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-${OS}-${ARCH}"
  chmod +x "${BIN_DIR}/kind"
else
  echo "✓ kind already installed"
fi

### ---- kubebuilder -----------------------------------------
KUBEBUILDER_VERSION=v4.5.0

if ! command -v kubebuilder >/dev/null 2>&1; then
  echo "▶ Installing kubebuilder ${KUBEBUILDER_VERSION}"
  curl -fLo "${BIN_DIR}/kubebuilder" \
    "https://go.kubebuilder.io/dl/latest/${OS}/${ARCH}"
  chmod +x "${BIN_DIR}/kubebuilder"
else
  echo "✓ kubebuilder already installed"
fi

### ---- kind cluster ----------------------------------------
# CLUSTER_NAME=kubetasker

# if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
#   echo "▶ Creating kind cluster '${CLUSTER_NAME}'"
#   kind create cluster --name "${CLUSTER_NAME}"
# else
#   echo "✓ kind cluster '${CLUSTER_NAME}' already exists"
# fi

### ---- Sanity checks ---------------------------------------
echo
echo "▶ Tool versions"
kind version
kubebuilder version
docker --version
go version
kubectl version --client
helm version
