package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	versionCheckClient = &http.Client{Timeout: 5 * time.Second}
	downloadClient     = &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
			TLSHandshakeTimeout:  10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
		},
	}
)

const npmRegistry = "https://registry.npmjs.org"

// npmRegistryURL is the base URL used for API calls; tests can override it.
var npmRegistryURL = npmRegistry

var npmPlatformPackages = map[string]string{
	"darwin/arm64":  "@blocks-network/cli-darwin-arm64",
	"darwin/amd64":  "@blocks-network/cli-darwin-x64",
	"linux/arm64":   "@blocks-network/cli-linux-arm64",
	"linux/amd64":   "@blocks-network/cli-linux-x64",
	"freebsd/arm64": "@blocks-network/cli-freebsd-arm64",
	"freebsd/amd64": "@blocks-network/cli-freebsd-x64",
	"windows/amd64": "@blocks-network/cli-win32-x64",
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Check for and install CLI updates",
	Long:  "Check npm for a newer version of the Blocks CLI and self-update.",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("Current version: %s\n", Version)

		latest, err := fetchLatestNpmVersion()
		if err != nil {
			return fmt.Errorf("could not check for updates: %w", err)
		}

		current := strings.TrimPrefix(Version, "v")
		latest = strings.TrimPrefix(latest, "v")

		if current == latest {
			fmt.Println("Already up to date.")
			return nil
		}

		if current == "dev" {
			fmt.Printf("Latest release: %s\n", latest)
			fmt.Println("You are running a development build. Upgrading to the latest release...")
		} else {
			fmt.Printf("New version available: %s (current: %s)\n", latest, current)
			fmt.Println("Downloading...")
		}

		if err := downloadAndInstall(latest); err != nil {
			return fmt.Errorf("upgrade failed: %w", err)
		}

		fmt.Printf("Successfully upgraded to %s\n", latest)
		return nil
	},
}

type npmPackageVersion struct {
	Version string `json:"version"`
	Dist    struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

func fetchLatestNpmVersion() (string, error) {
	url := fmt.Sprintf("%s/@blocks-network/cli/latest", npmRegistryURL)
	resp, err := versionCheckClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm registry returned %d", resp.StatusCode)
	}

	var pkg npmPackageVersion
	if err := json.NewDecoder(resp.Body).Decode(&pkg); err != nil {
		return "", fmt.Errorf("parse npm response: %w", err)
	}
	return pkg.Version, nil
}

func platformPackage() (string, error) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	pkg, ok := npmPlatformPackages[key]
	if !ok {
		return "", fmt.Errorf("in-CLI upgrade is not available for %s\n"+
			"Please update using the install script:\n"+
			"  curl -fsSL https://config.blocks.ai/install.sh | bash", key)
	}
	return pkg, nil
}

func fetchPlatformPackageMeta(pkg, version string) (npmPackageVersion, error) {
	url := fmt.Sprintf("%s/%s/%s", npmRegistryURL, pkg, version)
	resp, err := versionCheckClient.Get(url)
	if err != nil {
		return npmPackageVersion{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return npmPackageVersion{}, fmt.Errorf("npm registry returned %d for %s@%s", resp.StatusCode, pkg, version)
	}

	var meta npmPackageVersion
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return npmPackageVersion{}, fmt.Errorf("parse platform package metadata: %w", err)
	}
	return meta, nil
}

func downloadAndInstall(version string) error {
	pkg, err := platformPackage()
	if err != nil {
		return err
	}

	meta, err := fetchPlatformPackageMeta(pkg, version)
	if err != nil {
		return fmt.Errorf("fetch package metadata: %w", err)
	}

	if meta.Dist.Tarball == "" {
		return fmt.Errorf("registry did not provide a tarball URL for %s@%s", pkg, version)
	}

	tarballBytes, err := downloadTarball(meta.Dist.Tarball)
	if err != nil {
		return err
	}

	if err := verifyIntegrity(tarballBytes, meta.Dist.Integrity); err != nil {
		return err
	}

	binaryName := "blocks"
	if runtime.GOOS == "windows" {
		binaryName = "blocks.exe"
	}

	binary, err := extractBinaryFromTarball(bytes.NewReader(tarballBytes), binaryName)
	if err != nil {
		return err
	}

	return installBinary(binary)
}

func downloadTarball(tarballURL string) ([]byte, error) {
	resp, err := downloadClient.Get(tarballURL)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download: %w", err)
	}
	return data, nil
}

func verifyIntegrity(data []byte, sri string) error {
	if sri == "" {
		return fmt.Errorf("integrity check failed: no integrity hash provided by registry")
	}

	parts := strings.SplitN(sri, "-", 2)
	if len(parts) != 2 {
		return fmt.Errorf("integrity check failed: malformed SRI string %q", sri)
	}

	algo, expectedHash := parts[0], parts[1]
	if algo != "sha512" {
		return fmt.Errorf("integrity check failed: unsupported algorithm %q (expected sha512)", algo)
	}

	digest := sha512.Sum512(data)
	actualHash := base64.StdEncoding.EncodeToString(digest[:])

	if actualHash != expectedHash {
		return fmt.Errorf("integrity check failed: tarball hash does not match registry (expected %s...)", expectedHash[:16])
	}

	return nil
}

func extractBinaryFromTarball(r io.Reader, binaryName string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tarball: %w", err)
		}

		// Binary is at package/<binaryName> inside the tarball
		if filepath.Base(hdr.Name) == binaryName && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read binary from tarball: %w", err)
			}
			return data, nil
		}
	}

	return nil, fmt.Errorf("binary %q not found in tarball", binaryName)
}

func resolveInstallDir() (string, error) {
	if dir := os.Getenv("BLOCKS_INSTALL_DIR"); dir != "" {
		return dir, nil
	}

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err == nil {
		if isNpmManagedPath(exe) {
			return "", fmt.Errorf("this binary was installed via npm\n" +
				"Please upgrade using npm instead:\n" +
				"  npm i -g @blocks-network/cli@latest")
		}
		return filepath.Dir(exe), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine install directory: %w", err)
	}
	return filepath.Join(home, ".blocks", "bin"), nil
}

func isNpmManagedPath(exe string) bool {
	return strings.Contains(exe, filepath.Join("node_modules", "@blocks-network"))
}

func installBinary(binary []byte) error {
	installDir, err := resolveInstallDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	binaryName := "blocks"
	if runtime.GOOS == "windows" {
		binaryName = "blocks.exe"
	}

	dest := filepath.Join(installDir, binaryName)
	tmpDest := dest + ".tmp"

	if err := os.WriteFile(tmpDest, binary, 0o755); err != nil {
		return fmt.Errorf("write binary: %w", err)
	}

	if err := replaceBinary(tmpDest, dest); err != nil {
		return err
	}

	fmt.Printf("Installed to %s\n", dest)
	return nil
}

func replaceBinary(tmpDest, dest string) error {
	if runtime.GOOS == "windows" {
		oldDest := dest + ".old"
		os.Remove(oldDest)
		if err := os.Rename(dest, oldDest); err != nil && !os.IsNotExist(err) {
			os.Remove(tmpDest)
			return fmt.Errorf("move existing binary aside: %w", err)
		}
		if err := os.Rename(tmpDest, dest); err != nil {
			os.Remove(tmpDest)
			os.Rename(oldDest, dest) // rollback
			return fmt.Errorf("replace binary: %w", err)
		}
		return nil
	}

	if err := os.Rename(tmpDest, dest); err != nil {
		os.Remove(tmpDest)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}
