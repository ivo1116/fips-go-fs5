#!/bin/bash
# Build FIPS-compliant Go using golang-fips/go inside cflinuxfs5 container
# This script builds Go that links against system OpenSSL for FIPS compliance
#
# Usage: ./build-go-fips.sh [GO_VERSION]
#   GO_VERSION: Version tag to build (e.g., go1.22.12-1-openssl-fips)
#
# Available versions can be found at: https://github.com/golang-fips/go/releases
#
# Requirements:
# - Docker
# - Internet access

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTDIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Default to latest supported versions
GO_TAG="${1:-go1.22.12-1-openssl-fips}"
# Extract base version (e.g., 1.22.12 from go1.22.12-1-openssl-fips)
GO_VERSION=$(echo "$GO_TAG" | sed 's/go\([0-9.]*\).*/\1/')
OUTPUT_DIR="${ROOTDIR}/build/go-fips"

echo "========================================"
echo "Building FIPS Go from tag: ${GO_TAG}"
echo "Go Version: ${GO_VERSION}"
echo "========================================"

mkdir -p "${OUTPUT_DIR}"

# Create a build script that will run inside the container
cat > /tmp/build-fips-go-inner.sh << 'INNER_SCRIPT'
#!/bin/bash
set -euo pipefail

GO_TAG="$1"
GO_VERSION="$2"
echo "-----> Building FIPS Go from tag ${GO_TAG} (version ${GO_VERSION}) inside cflinuxfs5"

# Install build dependencies
apt-get update
apt-get install -y \
    git \
    build-essential \
    libssl-dev \
    wget \
    curl \
    ca-certificates \
    golang \
    --no-install-recommends

# Install a bootstrap Go (needed to build Go)
echo "-----> Installing bootstrap Go"
BOOTSTRAP_GO_VERSION="1.22.5"
cd /tmp
wget -q "https://go.dev/dl/go${BOOTSTRAP_GO_VERSION}.linux-amd64.tar.gz"
tar -C /usr/local -xzf "go${BOOTSTRAP_GO_VERSION}.linux-amd64.tar.gz"
export PATH="/usr/local/go/bin:$PATH"
export GOROOT="/usr/local/go"
go version

# Clone golang-fips/go at the specific release tag
echo "-----> Cloning golang-fips/go at tag ${GO_TAG}"
cd /tmp
git clone --branch "${GO_TAG}" --depth 1 https://github.com/golang-fips/go.git go-fips
cd go-fips

# The release tags contain the pre-patched source
# We need to initialize the submodule and build
echo "-----> Initializing repository"
if [ -f "scripts/create-secondary-patch.sh" ]; then
    # Some versions have this script
    ./scripts/create-secondary-patch.sh || true
fi

# Check if go submodule needs initialization
if [ -d "go" ] && [ ! -f "go/src/make.bash" ]; then
    echo "-----> Go directory exists but seems empty, initializing submodule"
    git submodule update --init --recursive
fi

# If go directory doesn't exist or is empty, we need to set it up
if [ ! -f "go/src/make.bash" ]; then
    echo "-----> Setting up Go source"
    # Check for setup scripts
    if [ -f "scripts/setup-go-submodule.sh" ]; then
        ./scripts/setup-go-submodule.sh || true
    fi

    if [ -f "scripts/full-initialize-repo.sh" ]; then
        ./scripts/full-initialize-repo.sh || true
    fi
fi

# At this point we should have a patched Go source
if [ ! -f "go/src/make.bash" ]; then
    echo "ERROR: go/src/make.bash not found after initialization"
    ls -la go/ || echo "go directory not found"
    exit 1
fi

# Build Go
echo "-----> Building Go"
cd go/src

# Set required environment variables for the build
export GOROOT_BOOTSTRAP="/usr/local/go"
export CGO_ENABLED=1

./make.bash

# Verify the build
echo "-----> Verifying FIPS build"
cd ..
./bin/go version

# Check for FIPS/OpenSSL symbols
echo "-----> Checking for FIPS symbols"
if ./bin/go tool nm ./bin/go 2>/dev/null | grep -qE "openssl|_goboringcrypto|FIPS"; then
    echo "-----> SUCCESS: FIPS/OpenSSL crypto symbols detected"
else
    echo "WARNING: No FIPS crypto symbols detected in go binary"
    echo "This may be expected - symbols are loaded dynamically at runtime"
fi

# Package the Go distribution
echo "-----> Packaging Go ${GO_VERSION} FIPS"
cd /tmp/go-fips
OUTPUT_NAME="go_${GO_VERSION}-fips_linux_x64_cflinuxfs5.tgz"

# Create tarball - only include the go directory
tar czf "/output/${OUTPUT_NAME}" go/

# Calculate SHA256
cd /output
sha256sum "${OUTPUT_NAME}" > "${OUTPUT_NAME}.sha256"
echo "-----> Package created: ${OUTPUT_NAME}"
cat "${OUTPUT_NAME}.sha256"

echo "-----> Build complete"
INNER_SCRIPT

chmod +x /tmp/build-fips-go-inner.sh

# Run the build inside cflinuxfs5 container
echo "-----> Starting Docker build..."
docker run --rm \
    --platform linux/amd64 \
    -v "${OUTPUT_DIR}:/output" \
    -v "/tmp/build-fips-go-inner.sh:/build.sh:ro" \
    cloudfoundry/cflinuxfs5 \
    bash /build.sh "${GO_TAG}" "${GO_VERSION}"

echo ""
echo "========================================"
echo "Build artifacts in: ${OUTPUT_DIR}"
echo "========================================"
ls -la "${OUTPUT_DIR}"

# Display SHA256 for manifest.yml
echo ""
echo "Add this to manifest.yml:"
echo "----------------------------------------"
SHA256=$(cat "${OUTPUT_DIR}/go_${GO_VERSION}-fips_linux_x64_cflinuxfs5.tgz.sha256" | awk '{print $1}')
echo "- name: go"
echo "  version: ${GO_VERSION}"
echo "  uri: https://github.com/ivo1116/fips-go-fs5/releases/download/v1.0.0/go_${GO_VERSION}-fips_linux_x64_cflinuxfs5.tgz"
echo "  sha256: ${SHA256}"
echo "  cf_stacks:"
echo "  - cflinuxfs5"
echo "  source: https://github.com/golang-fips/go/releases/tag/${GO_TAG}"
echo "----------------------------------------"
