package data

import (
	"fmt"
	"path"
)

func ReleaseYAML(mainPackageName string) string {
	release := `---
default_process_types:
    web: ./bin/%s
`
	return fmt.Sprintf(release, path.Base(mainPackageName))
}

func GoScript() string {
	return "PATH=$PATH:$HOME/bin\n"
}

func GoRootScript(goRoot string) string {
	contents := `export GOROOT=%s
PATH=$PATH:$GOROOT/bin
`

	return fmt.Sprintf(contents, goRoot)
}

func ZZGoPathScript(mainPackageName string) string {
	contents := `export GOPATH=$HOME
cd $GOPATH/src/%s
`
	return fmt.Sprintf(contents, path.Base(mainPackageName))
}

// FIPSScript returns the profile.d script content for FIPS configuration
func FIPSScript() string {
	return `# FIPS Configuration for golang-fips/go
# This enables FIPS mode for Go applications using system OpenSSL

export GOLANG_FIPS=1

# Verify FIPS mode is available
if [ -f /proc/sys/crypto/fips_enabled ]; then
  FIPS_ENABLED=$(cat /proc/sys/crypto/fips_enabled)
  if [ "$FIPS_ENABLED" = "1" ]; then
    echo "-----> FIPS mode enabled (kernel)"
  fi
fi
`
}
