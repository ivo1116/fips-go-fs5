#!/bin/bash
# Build FIPS-compliant Go using golang-fips/go inside cflinuxfs5 container
# This script builds Go that links against system OpenSSL for FIPS compliance
#
# Usage: ./build-go-fips.sh [GO_VERSION]
#   GO_VERSION: Version to build (e.g., 1.22.10, 1.23.4). Default: 1.22.10
#
# The script will:
# 1. Clone golang-fips/go repository
# 2. Initialize the repository with the specified version
# 3. Build Go with OpenSSL patches
# 4. Package the result as a tarball
#
# Requirements:
# - Docker
# - Internet access (to clone repos and download dependencies)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTDIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Default Go version - match golang-fips/go supported versions
GO_VERSION="${1:-1.22.10}"
OUTPUT_DIR="${ROOTDIR}/build/go-fips"

echo "========================================"
echo "Building FIPS Go ${GO_VERSION}"
echo "========================================"

mkdir -p "${OUTPUT_DIR}"

# Create a build script that will run inside the container
cat > /tmp/build-fips-go-inner.sh << 'INNER_SCRIPT'
#!/bin/bash
set -euo pipefail

GO_VERSION="$1"
echo "-----> Building FIPS Go ${GO_VERSION} inside cflinuxfs5"

# Install build dependencies
apt-get update
apt-get install -y \
    git \
    build-essential \
    libssl-dev \
    wget \
    curl \
    ca-certificates \
    --no-install-recommends

# Clone golang-fips/go
echo "-----> Cloning golang-fips/go"
cd /tmp
git clone https://github.com/golang-fips/go.git go-fips
cd go-fips

# Check out the appropriate branch for the version
# golang-fips/go uses go1.XX.Y branches
BRANCH="go${GO_VERSION}"
echo "-----> Checking out branch/tag for version ${GO_VERSION}"

# First try to find exact tag or branch
if git rev-parse "refs/tags/${BRANCH}" >/dev/null 2>&1; then
    git checkout "${BRANCH}"
elif git rev-parse "refs/remotes/origin/${BRANCH}" >/dev/null 2>&1; then
    git checkout "${BRANCH}"
else
    # Try main branch and specify version via environment
    echo "-----> Using main branch with version specification"
    git checkout main
fi

# Initialize the repository - this applies the FIPS patches
echo "-----> Initializing golang-fips/go repository"
if [ -f "./scripts/full-initialize-repo.sh" ]; then
    # Set the Go version to fetch
    export GOLANG_VERSION="${GO_VERSION}"
    ./scripts/full-initialize-repo.sh "${GO_VERSION}" || ./scripts/full-initialize-repo.sh
elif [ -f "./scripts/setup-initial-patches.sh" ]; then
    ./scripts/setup-initial-patches.sh
fi

# Build Go
echo "-----> Building Go"
cd go/src
./make.bash

# Verify the build includes OpenSSL support
echo "-----> Verifying FIPS build"
cd ..
if ./bin/go version; then
    echo "Go built successfully"
fi

# Check for FIPS/boring crypto symbols
if ./bin/go tool nm ./bin/go 2>/dev/null | grep -q "_goboringcrypto\|FIPS"; then
    echo "-----> FIPS crypto symbols detected"
else
    echo "WARNING: No FIPS crypto symbols detected in go binary"
fi

# Package the Go distribution
echo "-----> Packaging Go ${GO_VERSION} FIPS"
cd /tmp/go-fips
OUTPUT_NAME="go_${GO_VERSION}-fips_linux_x64_cflinuxfs5.tgz"
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
    -v "${OUTPUT_DIR}:/output" \
    -v "/tmp/build-fips-go-inner.sh:/build.sh:ro" \
    cloudfoundry/cflinuxfs5 \
    bash /build.sh "${GO_VERSION}"

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
echo "  source: https://github.com/golang-fips/go"
echo "----------------------------------------"
