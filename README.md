# FIPS Go Buildpack for Cloud Foundry (cflinuxfs5)

A FIPS-compliant Go buildpack for Cloud Foundry that uses [golang-fips/go](https://github.com/golang-fips/go) (Red Hat's fork) to provide Go runtimes that link against system OpenSSL for FIPS 140-2 validated cryptography.

## Overview

This buildpack is designed for applications that require FIPS 140-2 compliance. It uses the golang-fips/go fork of Go, which patches the standard Go crypto libraries to use system OpenSSL instead of Go's native crypto implementations.

### Key Features

- **FIPS 140-2 Compliance**: Cryptographic operations use system OpenSSL (FIPS-validated on cflinuxfs5-fips)
- **Production Ready**: Based on Red Hat's golang-fips/go, used in RHEL/CentOS/Fedora
- **Drop-in Replacement**: Compatible with standard Go applications
- **Stack**: Targets cflinuxfs5 (Ubuntu 24.04 Noble)

## Supported Go Versions

| Version | Status | Notes |
|---------|--------|-------|
| 1.22.x | Supported | LTS version |
| 1.23.x | Supported | Current stable |

## Usage

### Pushing an Application

```bash
# Use the FIPS Go buildpack
cf push myapp -b https://github.com/ivo1116/fips-go-fs5/releases/download/v1.0.0/go_buildpack_fips-v1.0.0.zip -s cflinuxfs5

# Or specify a custom buildpack name if installed in CF
cf push myapp -b go_buildpack_fips -s cflinuxfs5
```

### Specifying Go Version

Create a `go.mod` file (recommended) or set the `GOVERSION` environment variable:

```bash
cf set-env myapp GOVERSION go1.22.10
cf restage myapp
```

### FIPS Environment Variables

The buildpack automatically sets:

```bash
GOLANG_FIPS=1              # Enables FIPS mode in golang-fips/go
OPENSSL_FORCE_FIPS_MODE=1  # Forces OpenSSL FIPS mode
```

## How It Works

1. **Supply Phase**: Downloads and installs FIPS Go runtime (golang-fips/go)
2. **Finalize Phase**: Compiles your application with FIPS-compliant crypto
3. **Runtime**: Application uses system OpenSSL for all crypto operations

### FIPS Verification

The buildpack verifies FIPS compilation by checking for BoringCrypto/FIPS symbols:

```
-----> Verifying FIPS compilation
-----> myapp: Compiled with FIPS crypto support
```

## Building FIPS Go Binaries

To build new Go versions with FIPS support:

```bash
# Build Go 1.22.10 with FIPS
./scripts/build-go-fips.sh 1.22.10

# Build Go 1.23.4 with FIPS
./scripts/build-go-fips.sh 1.23.4

# Output will be in build/go-fips/
```

The script:
1. Clones golang-fips/go
2. Initializes with FIPS patches
3. Builds Go inside cflinuxfs5 container
4. Packages as tarball with SHA256

## Packaging the Buildpack

```bash
# Build the buildpack binaries
./scripts/build.sh

# Package as zip
./scripts/package.sh --version 1.0.0-fips --stack cflinuxfs5
```

## Verification

### Check FIPS Mode at Runtime

```bash
cf ssh myapp -c "cat /proc/sys/crypto/fips_enabled"
# Output: 1 (if kernel FIPS mode is enabled)
```

### Verify Application Uses FIPS Crypto

Create a test application:

```go
package main

import (
    "crypto/sha256"
    "fmt"
)

func main() {
    h := sha256.New()
    h.Write([]byte("FIPS test"))
    fmt.Printf("SHA256 (FIPS): %x\n", h.Sum(nil))
}
```

Push and verify:

```bash
cf push fips-test -b go_buildpack_fips -s cflinuxfs5
cf logs fips-test --recent
```

## Architecture

```
fips-go-fs5/
├── bin/
│   ├── compile      # Main compilation entry point
│   ├── detect       # Detects Go applications
│   ├── supply       # Installs Go runtime
│   ├── finalize     # Compiles application
│   └── release      # Release configuration
├── src/go/
│   ├── supply/      # Supply phase logic (FIPS env setup)
│   ├── finalize/    # Finalize phase logic (FIPS verification)
│   ├── data/        # Profile.d script templates
│   └── hooks/       # APM integration hooks
├── scripts/
│   ├── build.sh           # Build buildpack binaries
│   ├── package.sh         # Package buildpack zip
│   └── build-go-fips.sh   # Build FIPS Go binaries
├── manifest.yml     # Dependency definitions
├── config.json      # Build configuration
└── VERSION
```

## Requirements

- **Stack**: cflinuxfs5 (required for FIPS OpenSSL)
- **CF CLI**: v7 or later recommended
- **Docker**: Required for building FIPS Go binaries

## Troubleshooting

### "No FIPS symbols detected"

This warning appears if your application doesn't use crypto packages. It's informational only.

### Build Failures

Check that:
1. Your application has a valid `go.mod` file
2. The stack is set to `cflinuxfs5`
3. Go version is supported (1.22.x or 1.23.x)

### Runtime Crypto Errors

Ensure the cflinuxfs5-fips rootfs is being used with FIPS-enabled OpenSSL.

## License

Apache License 2.0

## Related Projects

- [golang-fips/go](https://github.com/golang-fips/go) - FIPS-compliant Go fork
- [Cloud Foundry Go Buildpack](https://github.com/cloudfoundry/go-buildpack) - Upstream buildpack
- [cflinuxfs5](https://github.com/cloudfoundry/cflinuxfs5) - Ubuntu 24.04 rootfs
