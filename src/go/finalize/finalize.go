package finalize

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver"
	"github.com/ivo1116/fips-go-fs5/src/go/data"
	"github.com/ivo1116/fips-go-fs5/src/go/godep"
	"github.com/ivo1116/fips-go-fs5/src/go/warnings"
	"github.com/cloudfoundry/libbuildpack"
)

type Command interface {
	Execute(string, io.Writer, io.Writer, string, ...string) error
}

type BuildpackConfig struct {
	LDFlags map[string]string `yaml:"ldflags"`
}

type Stager interface {
	BuildDir() string
	CacheDir() string
	ClearDepDir() error
	DepDir() string
	DepsIdx() string
	WriteProfileD(string, string) error
}

type Finalizer struct {
	Stager           Stager
	Command          Command
	Log              *libbuildpack.Logger
	VendorTool       string
	GoVersion        string
	Godep            godep.Godep
	MainPackageName  string
	GoPath           string
	PackageList      []string
	BuildFlags       []string
	VendorExperiment bool
}

func NewFinalizer(stager Stager, command Command, logger *libbuildpack.Logger) (*Finalizer, error) {
	config := struct {
		Config struct {
			GoVersion  string `yaml:"GoVersion"`
			VendorTool string `yaml:"VendorTool"`
			Godep      string `yaml:"Godep"`
			FIPS       string `yaml:"FIPS"`
		} `yaml:"config"`
	}{}
	if err := libbuildpack.NewYAML().Load(filepath.Join(stager.DepDir(), "config.yml"), &config); err != nil {
		logger.Error("Unable to read config.yml: %s", err)
		return nil, err
	}

	var godep godep.Godep
	if config.Config.VendorTool == "godep" {
		if err := json.Unmarshal([]byte(config.Config.Godep), &godep); err != nil {
			logger.Error("Unable to load config Godep json: %s", err)
			return nil, err
		}
	}

	if config.Config.FIPS == "enabled" {
		logger.Info("-----> FIPS mode is enabled for this build")
	}

	return &Finalizer{
		Stager:     stager,
		Command:    command,
		Log:        logger,
		Godep:      godep,
		GoVersion:  config.Config.GoVersion,
		VendorTool: config.Config.VendorTool,
	}, nil
}

func Run(gf *Finalizer) error {
	var config struct {
		Go BuildpackConfig `yaml:"go"`
	}
	config.Go.LDFlags = map[string]string{}

	buildpackYAMLPath := filepath.Join(gf.Stager.BuildDir(), "buildpack.yml")
	ok, err := libbuildpack.FileExists(buildpackYAMLPath)
	if err != nil {
		gf.Log.Error("Unable to stat buildpack.yml: %s", err)
		return err
	}

	if ok {
		if err := libbuildpack.NewYAML().Load(buildpackYAMLPath, &config); err != nil {
			gf.Log.Error("Unable to parse buildpack.yml: %s", err)
			return err
		}
	}

	if err := gf.SetGoCache(); err != nil {
		gf.Log.Error("Unable to print gocache location: %s", err)
		return err
	}

	if err := gf.SetMainPackageName(); err != nil {
		gf.Log.Error("Unable to determine import path: %s", err)
		return err
	}

	if err := os.MkdirAll(filepath.Join(gf.Stager.BuildDir(), "bin"), 0755); err != nil {
		gf.Log.Error("Unable to create <build-dir>/bin: %s", err)
		return err
	}

	if gf.VendorTool != "gomod" {
		if err := gf.SetupGoPath(); err != nil {
			gf.Log.Error("Unable to setup Go path: %s", err)
			return err
		}
	} else {
		if err := os.Setenv("GOBIN", filepath.Join(gf.Stager.BuildDir(), "bin")); err != nil {
			gf.Log.Error("Unable to setup GOBIN: %s", err)
			return err
		}
	}

	if err := gf.HandleVendorExperiment(); err != nil {
		gf.Log.Error("Invalid vendor config: %s", err)
		return err
	}

	if gf.VendorTool == "glide" {
		if err := gf.RunGlideInstall(); err != nil {
			gf.Log.Error("Error running 'glide install': %s", err)
			return err
		}
	} else if gf.VendorTool == "dep" {
		if err := gf.RunDepEnsure(); err != nil {
			gf.Log.Error("Error running 'dep ensure': %s", err)
			return err
		}
	}

	gf.SetBuildFlags(config.Go)

	if err := gf.SetInstallPackages(); err != nil {
		gf.Log.Error("Unable to determine packages to install: %s", err)
		return err
	}

	if err := gf.CompileApp(); err != nil {
		gf.Log.Error("Unable to compile application: %s", err)
		return err
	}

	// Verify FIPS compilation
	gf.VerifyFIPSCompilation()

	if err := gf.CreateStartupEnvironment("/tmp"); err != nil {
		gf.Log.Error("Unable to create startup scripts: %s", err)
		return err
	}

	return nil
}

// VerifyFIPSCompilation checks if the compiled binary has FIPS symbols
func (gf *Finalizer) VerifyFIPSCompilation() {
	gf.Log.BeginStep("Verifying FIPS compilation")

	binDir := filepath.Join(gf.Stager.BuildDir(), "bin")
	files, err := ioutil.ReadDir(binDir)
	if err != nil {
		gf.Log.Warning("Could not read bin directory for FIPS verification: %s", err)
		return
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		binaryPath := filepath.Join(binDir, file.Name())
		buffer := new(bytes.Buffer)
		errorBuffer := new(bytes.Buffer)

		// Use go tool nm to check for FIPS symbols
		goPath := filepath.Join(gf.Stager.DepDir(), "go"+gf.GoVersion, "go", "bin", "go")
		err := gf.Command.Execute(gf.Stager.BuildDir(), buffer, errorBuffer, goPath, "tool", "nm", binaryPath)
		if err != nil {
			gf.Log.Warning("Could not check FIPS symbols in %s: %s", file.Name(), err)
			continue
		}

		output := buffer.String()
		if strings.Contains(output, "_goboringcrypto") || strings.Contains(output, "FIPS") {
			gf.Log.Info("-----> %s: Compiled with FIPS crypto support", file.Name())
		} else {
			gf.Log.Warning("%s: No FIPS symbols detected (app may not use crypto)", file.Name())
		}
	}
}

func (gf *Finalizer) SetMainPackageName() error {
	switch gf.VendorTool {
	case "godep":
		gf.MainPackageName = gf.Godep.ImportPath

	case "glide":
		buffer := new(bytes.Buffer)
		errorBuffer := new(bytes.Buffer)

		if err := gf.Command.Execute(gf.Stager.BuildDir(), buffer, errorBuffer, "glide", "name"); err != nil {
			gf.Log.Error("problem retrieving main package name: %s", errorBuffer)
			return err
		}
		gf.MainPackageName = strings.TrimSpace(buffer.String())
	case "dep":
		fallthrough
	case "go_nativevendoring":
		gf.MainPackageName = os.Getenv("GOPACKAGENAME")
		if gf.MainPackageName == "" {
			gf.Log.Error(warnings.NoGOPACKAGENAMEerror())
			return errors.New("GOPACKAGENAME unset")
		}
	case "gomod":
		buffer := new(bytes.Buffer)
		errorBuffer := new(bytes.Buffer)

		if err := gf.Command.Execute(gf.Stager.BuildDir(), buffer, errorBuffer, "go", "list", "-m"); err != nil {
			gf.Log.Error("problem retrieving main package name: %s", errorBuffer)
			return err
		}
		gf.MainPackageName = strings.TrimSpace(buffer.String())
	default:
		return errors.New("invalid vendor tool")
	}
	return nil
}

func (gf *Finalizer) SetGoCache() error {
	return os.Setenv("GOCACHE", filepath.Join(gf.Stager.CacheDir(), "go-cache"))
}

func (gf *Finalizer) SetupGoPath() error {
	var skipMoveFile = map[string]bool{
		".cloudfoundry": true,
		"Procfile":      true,
		".profile":      true,
		"src":           true,
		".profile.d":    true,
	}

	var goPath string
	goPathInImage := os.Getenv("GO_SETUP_GOPATH_IN_IMAGE") == "true"

	if goPathInImage {
		goPath = gf.Stager.BuildDir()
	} else {
		tmpDir, err := ioutil.TempDir("", "gobuildpack.gopath")
		if err != nil {
			return err
		}
		goPath = filepath.Join(tmpDir, ".go")
	}

	err := os.Setenv("GOPATH", goPath)
	if err != nil {
		return err
	}
	gf.GoPath = goPath

	binDir := filepath.Join(gf.Stager.BuildDir(), "bin")
	err = os.MkdirAll(binDir, 0755)
	if err != nil {
		return err
	}

	packageDir := gf.mainPackagePath()
	err = os.MkdirAll(packageDir, 0755)
	if err != nil {
		return err
	}

	if goPathInImage {
		files, err := ioutil.ReadDir(gf.Stager.BuildDir())
		if err != nil {
			return err
		}
		for _, f := range files {
			if !skipMoveFile[f.Name()] {
				src := filepath.Join(gf.Stager.BuildDir(), f.Name())
				dest := filepath.Join(packageDir, f.Name())

				err = os.Rename(src, dest)
				if err != nil {
					return err
				}
			}
		}
	} else {
		err = os.Setenv("GOBIN", binDir)
		if err != nil {
			return err
		}

		err = libbuildpack.CopyDirectory(gf.Stager.BuildDir(), packageDir)
		if err != nil {
			return err
		}
	}
	// unset git dir or it will mess with go install
	return os.Unsetenv("GIT_DIR")
}

func (gf *Finalizer) SetBuildFlags(config BuildpackConfig) {
	flags := []string{"-tags", "cloudfoundry", "-buildmode", "pie"}

	if os.Getenv("GO_LINKER_SYMBOL") != "" && os.Getenv("GO_LINKER_VALUE") != "" {
		config.LDFlags[os.Getenv("GO_LINKER_SYMBOL")] = os.Getenv("GO_LINKER_VALUE")
	}

	if len(config.LDFlags) > 0 {
		var ldflags []string
		for key, val := range config.LDFlags {
			ldflags = append(ldflags, fmt.Sprintf("-X %s=%s", key, val))
		}
		flags = append(flags, "-ldflags", strings.Join(ldflags, " "))
	}

	gf.BuildFlags = flags
	return
}

func (gf *Finalizer) RunDepEnsure() error {
	vendorDirExists, err := libbuildpack.FileExists(filepath.Join(gf.mainPackagePath(), "vendor"))
	if err != nil {
		return err
	}
	runEnsure := true

	if vendorDirExists {
		numSubDirs := 0
		files, err := ioutil.ReadDir(filepath.Join(gf.mainPackagePath(), "vendor"))
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.IsDir() {
				numSubDirs++
			}
		}

		if numSubDirs > 0 {
			runEnsure = false
		}
	}

	if runEnsure {
		gf.Log.BeginStep("Fetching any unsaved dependencies (dep ensure)")

		if err := gf.Command.Execute(gf.mainPackagePath(), os.Stdout, os.Stderr, "dep", "ensure"); err != nil {
			return err
		}
	} else {
		gf.Log.Info("Note: skipping (dep ensure) due to non-empty vendor directory.")
	}

	return nil
}

func (gf *Finalizer) RunGlideInstall() error {
	if gf.VendorTool != "glide" {
		return nil
	}

	vendorDirExists, err := libbuildpack.FileExists(filepath.Join(gf.mainPackagePath(), "vendor"))
	if err != nil {
		return err
	}
	runGlideInstall := true

	if vendorDirExists {
		numSubDirs := 0
		files, err := ioutil.ReadDir(filepath.Join(gf.mainPackagePath(), "vendor"))
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.IsDir() {
				numSubDirs++
			}
		}

		if numSubDirs > 0 {
			runGlideInstall = false
		}
	}

	if runGlideInstall {
		gf.Log.BeginStep("Fetching any unsaved dependencies (glide install)")

		if err := gf.Command.Execute(gf.mainPackagePath(), os.Stdout, os.Stderr, "glide", "install"); err != nil {
			return err
		}
	} else {
		gf.Log.Info("Note: skipping (glide install) due to non-empty vendor directory.")
	}

	return nil
}

func (gf *Finalizer) HandleVendorExperiment() error {
	gf.VendorExperiment = true

	if os.Getenv("GO15VENDOREXPERIMENT") == "" {
		return nil
	}

	ver, err := semver.NewVersion(gf.GoVersion)
	if err != nil {
		return err
	}

	go16 := ver.Major() == 1 && ver.Minor() == 6
	if !go16 {
		gf.Log.Error(warnings.UnsupportedGO15VENDOREXPERIMENTerror())
		return errors.New("unsupported GO15VENDOREXPERIMENT")
	}

	if os.Getenv("GO15VENDOREXPERIMENT") == "0" {
		gf.VendorExperiment = false
	}

	return nil
}

func (gf *Finalizer) SetInstallPackages() error {
	var packages []string

	if os.Getenv("GO_INSTALL_PACKAGE_SPEC") != "" {
		packages = append(packages, strings.Split(os.Getenv("GO_INSTALL_PACKAGE_SPEC"), " ")...)
	}

	vendorDirExists, err := libbuildpack.FileExists(filepath.Join(gf.mainPackagePath(), "vendor"))
	if err != nil {
		return err
	}

	if gf.VendorTool == "godep" {
		useVendorDir := gf.VendorExperiment && !gf.Godep.WorkspaceExists

		if gf.Godep.WorkspaceExists && vendorDirExists {
			gf.Log.Warning(warnings.GodepsWorkspaceWarning())
		}

		if useVendorDir && !vendorDirExists {
			gf.Log.Warning("vendor/ directory does not exist.")
		}

		if len(packages) != 0 {
			gf.Log.Warning(warnings.PackageSpecOverride(packages))
		} else if len(gf.Godep.Packages) != 0 {
			packages = gf.Godep.Packages
		} else {
			gf.Log.Warning("Installing package '.' (default)")
			packages = append(packages, ".")
		}

		if useVendorDir {
			packages = gf.updatePackagesForVendor(packages)
		}
	} else {
		if !gf.VendorExperiment && gf.VendorTool == "go_nativevendoring" {
			gf.Log.Error(warnings.MustUseVendorError())
			return errors.New("must use vendor/ for go native vendoring")
		}

		if len(packages) == 0 {
			packages = append(packages, ".")
			gf.Log.Warning("Installing package '.' (default)")
		}

		packages = gf.updatePackagesForVendor(packages)
	}

	gf.PackageList = packages
	return nil
}

func (gf *Finalizer) CompileApp() error {
	cmd := "go"

	// Use the FIPS Go binary directly to ensure we don't fall back to system Go.
	// Try multiple possible paths since the tarball extraction structure may vary.
	goDir := filepath.Join(gf.Stager.DepDir(), "go"+gf.GoVersion)
	candidatePaths := []string{
		filepath.Join(goDir, "go", "bin", "go"),  // tarball with go/ prefix
		filepath.Join(goDir, "bin", "go"),         // tarball without prefix
	}

	var fipsGoPath string
	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			fipsGoPath = p
			break
		}
	}

	if fipsGoPath != "" {
		cmd = fipsGoPath
		gf.Log.Info("-----> Using FIPS Go at %s", fipsGoPath)

		// Also set GOROOT and prepend to PATH to ensure all sub-tools use FIPS Go
		goRoot := filepath.Dir(filepath.Dir(fipsGoPath)) // strip /bin/go
		os.Setenv("GOROOT", goRoot)
		os.Setenv("PATH", filepath.Join(goRoot, "bin")+":"+os.Getenv("PATH"))
		gf.Log.Info("-----> Set GOROOT=%s", goRoot)

		// CRITICAL: Prevent Go 1.21+ toolchain auto-download feature from
		// replacing our FIPS Go with a standard Go version. If go.mod specifies
		// a newer Go version, Go would download and use that version instead.
		os.Setenv("GOTOOLCHAIN", "local")
		gf.Log.Info("-----> Set GOTOOLCHAIN=local (preventing auto-download of non-FIPS Go)")

		// CRITICAL: Enable CGO so golang-fips/go links against system OpenSSL.
		// Without CGO, Go uses native (non-FIPS) crypto even with the FIPS fork.
		os.Setenv("CGO_ENABLED", "1")
		gf.Log.Info("-----> Set CGO_ENABLED=1 (required for OpenSSL FIPS backend)")

		// Enable BoringCrypto experiment which activates the FIPS crypto backend.
		// This sets the 'boringcrypto' build tag that selects the BoringSSL/FIPS
		// implementation in crypto/internal/boring instead of Go native crypto.
		os.Setenv("GOEXPERIMENT", "boringcrypto")
		gf.Log.Info("-----> Set GOEXPERIMENT=boringcrypto")
	} else {
		// Log all candidates we tried for debugging
		gf.Log.Warning("FIPS Go not found at expected paths:")
		for _, p := range candidatePaths {
			gf.Log.Warning("  tried: %s", p)
		}
		// Also list what's actually in the go install dir
		if entries, err := os.ReadDir(goDir); err == nil {
			for _, e := range entries {
				gf.Log.Warning("  found: %s/%s (dir=%v)", goDir, e.Name(), e.IsDir())
			}
		} else {
			gf.Log.Warning("  cannot read %s: %s", goDir, err)
		}
	}

	args := []string{"install"}
	args = append(args, gf.BuildFlags...)
	args = append(args, gf.PackageList...)

	if gf.VendorTool == "godep" && (gf.Godep.WorkspaceExists || !gf.VendorExperiment) {
		args = append([]string{"go"}, args...)
		cmd = "godep"
	}

	gf.Log.BeginStep(fmt.Sprintf("Running: %s %s", cmd, strings.Join(args, " ")))

	err := gf.Command.Execute(gf.mainPackagePath(), os.Stdout, os.Stderr, cmd, args...)
	if err != nil {
		return err
	}
	return nil
}

func (gf *Finalizer) CreateStartupEnvironment(tempDir string) error {
	mainPkgName := gf.MainPackageName
	if len(gf.PackageList) > 0 && gf.PackageList[0] != "." {
		mainPkgName = filepath.Base(gf.PackageList[0])
	}

	err := ioutil.WriteFile(filepath.Join(tempDir, "buildpack-release-step.yml"), []byte(data.ReleaseYAML(mainPkgName)), 0644)
	if err != nil {
		gf.Log.Error("Unable to write release yml: %s", err)
		return err
	}

	// Save FIPS profile.d scripts before ClearDepDir wipes them
	var savedFIPSScript []byte
	fipsScriptPath := filepath.Join(gf.Stager.DepDir(), "profile.d", "fips.sh")
	if content, readErr := ioutil.ReadFile(fipsScriptPath); readErr == nil {
		savedFIPSScript = content
		gf.Log.Info("-----> Preserving FIPS profile.d script")
	}

	if os.Getenv("GO_INSTALL_TOOLS_IN_IMAGE") == "true" {
		goRuntimeLocation := filepath.Join("$DEPS_DIR", gf.Stager.DepsIdx(), "go"+gf.GoVersion, "go")

		gf.Log.BeginStep("Leaving go tool chain in $GOROOT=%s", goRuntimeLocation)

	} else {
		if err := gf.Stager.ClearDepDir(); err != nil {
			return err
		}
	}

	// Restore FIPS profile.d script after ClearDepDir
	if savedFIPSScript != nil {
		profileDDir := filepath.Join(gf.Stager.DepDir(), "profile.d")
		if err := os.MkdirAll(profileDDir, 0755); err != nil {
			return err
		}
		if err := ioutil.WriteFile(filepath.Join(profileDDir, "fips.sh"), savedFIPSScript, 0755); err != nil {
			gf.Log.Error("Unable to restore FIPS profile.d script: %s", err)
			return err
		}
		gf.Log.Info("-----> Restored FIPS profile.d script")
	}

	if os.Getenv("GO_SETUP_GOPATH_IN_IMAGE") == "true" {
		gf.Log.BeginStep("Cleaning up $GOPATH/pkg")
		if err := os.RemoveAll(filepath.Join(gf.GoPath, "pkg")); err != nil {
			return err
		}

		if err := gf.Stager.WriteProfileD("zzgopath.sh", data.ZZGoPathScript(mainPkgName)); err != nil {
			return err
		}
	}

	return gf.Stager.WriteProfileD("go.sh", data.GoScript())
}

func (gf *Finalizer) mainPackagePath() string {
	if gf.VendorTool == "gomod" {
		return gf.Stager.BuildDir()
	}
	return filepath.Join(gf.GoPath, "src", gf.MainPackageName)
}

func (gf *Finalizer) goInstallLocation() string {
	return filepath.Join(gf.Stager.DepDir(), "go"+gf.GoVersion)
}

func (gf *Finalizer) updatePackagesForVendor(packages []string) []string {
	var newPackages []string

	for _, pkg := range packages {
		vendored, _ := libbuildpack.FileExists(filepath.Join(gf.mainPackagePath(), "vendor", pkg))
		if pkg == "." || !vendored {
			newPackages = append(newPackages, pkg)
		} else {
			newPackages = append(newPackages, filepath.Join(gf.MainPackageName, "vendor", pkg))
		}
	}

	return newPackages
}
