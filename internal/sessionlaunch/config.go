package sessionlaunch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func Verify(ctx context.Context, dataRoot, provider, executable string) (Configuration, error) {
	if ctx == nil || !supportedProvider(provider) || !cleanAbsolute(executable) {
		return Configuration{}, errors.New("launcher verification request is invalid")
	}
	version, value, err := probeVerifiedExecutable(ctx, provider, executable)
	if err != nil {
		return Configuration{}, err
	}
	value.Version = version
	if err := SaveConfiguration(dataRoot, value); err != nil {
		return Configuration{}, err
	}
	return LoadConfiguration(dataRoot, provider)
}

type Configuration struct {
	SchemaVersion int                     `json:"schema_version" required:"true"`
	Provider      string                  `json:"provider" required:"true"`
	Version       string                  `json:"version" required:"true"`
	Executable    string                  `json:"executable" required:"true"`
	Identity      pathguard.IdentityToken `json:"identity" required:"true"`
	SHA256        string                  `json:"sha256" required:"true"`
}

func MeasureConfiguration(provider, version, executable string) (Configuration, error) {
	if !supportedProvider(provider) || strings.TrimSpace(version) == "" || len(version) > 256 || !cleanAbsolute(executable) {
		return Configuration{}, errors.New("launcher metadata is invalid")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil || !cleanAbsolute(resolved) {
		return Configuration{}, errors.New("launcher executable must be a physical absolute file")
	}
	executable = resolved
	file, err := os.Open(executable)
	if err != nil {
		return Configuration{}, errors.New("open launcher executable")
	}
	defer file.Close()
	return measureOpenConfiguration(provider, strings.TrimSpace(version), executable, file)
}

func measureOpenConfiguration(provider, version, executable string, file *os.File) (Configuration, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return Configuration{}, errors.New("launcher executable is not executable")
	}
	identity, err := pathguard.PhysicalFileIdentity(file)
	if err != nil {
		return Configuration{}, errors.New("measure launcher executable identity")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Configuration{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return Configuration{}, errors.New("measure launcher executable digest")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Configuration{}, err
	}
	return Configuration{SchemaVersion: 1, Provider: provider, Version: version, Executable: executable, Identity: identity, SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

func SaveConfiguration(dataRoot string, value Configuration) error {
	if err := validateDataRoot(dataRoot); err != nil {
		return err
	}
	if err := validateConfiguration(value); err != nil {
		return err
	}
	current, err := MeasureConfiguration(value.Provider, value.Version, value.Executable)
	if err != nil || current != value {
		return errors.Join(errors.New("launcher executable changed before save"), err)
	}
	body, err := strictjson.Encode(value)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dataRoot, configurationName(value.Provider)), body, 0o600)
}

func LoadConfiguration(dataRoot, provider string) (Configuration, error) {
	if err := validateDataRoot(dataRoot); err != nil {
		return Configuration{}, err
	}
	if !supportedProvider(provider) {
		return Configuration{}, errors.New("launcher provider is unsupported")
	}
	root, err := pathguard.Open(dataRoot)
	if err != nil {
		return Configuration{}, errors.New("launcher configuration root is unavailable or unsafe")
	}
	defer root.Close()
	body, err := readPrivateRootFile(root, configurationName(provider), 16<<10, 0o600)
	if err != nil {
		return Configuration{}, errors.Join(errors.New("launcher configuration is unavailable or unsafe"), err)
	}
	var value Configuration
	if err := strictjson.Decode(body, &value); err != nil {
		return Configuration{}, err
	}
	if err := validateConfiguration(value); err != nil || value.Provider != provider {
		return Configuration{}, errors.Join(errors.New("launcher configuration identity is invalid"), err)
	}
	current, err := MeasureConfiguration(value.Provider, value.Version, value.Executable)
	if err != nil || current != value {
		return Configuration{}, errors.Join(errors.New("configured launcher executable changed"), err)
	}
	return value, nil
}

func readPrivateRootFile(root *pathguard.Directory, leaf string, maximum int64, permission os.FileMode) ([]byte, error) {
	if root == nil || root.Root == nil {
		return nil, errors.New("private file root is unavailable")
	}
	before, err := root.Root.Lstat(leaf)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && before.Mode().Perm() != permission {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	return pathguard.ReadStableRegularRootFile(root.Root, leaf, before, maximum)
}

func configurationName(provider string) string { return "session-launcher-" + provider + ".json" }
func validateDataRoot(root string) error {
	if !cleanAbsolute(root) {
		return errors.New("launcher requires a clean absolute data root")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errors.Join(errors.New("launcher data root is unavailable"), err)
	}
	return nil
}
func validateConfiguration(value Configuration) error {
	if value.SchemaVersion != 1 || !supportedProvider(value.Provider) || strings.TrimSpace(value.Version) == "" || len(value.Version) > 256 || !cleanAbsolute(value.Executable) || !value.Identity.Valid() || len(value.SHA256) != 64 {
		return errors.New("launcher metadata is invalid")
	}
	for _, c := range value.SHA256 {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return errors.New("launcher digest is invalid")
		}
	}
	return nil
}
