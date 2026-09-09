package decisions

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const agentConfigurationName = "decision-agent.json"

// AgentConfiguration is written only after the proposal-only adapter verifies
// one concrete executable. It is private control state, never model input or a
// Project/Vault document.
type AgentConfiguration struct {
	SchemaVersion int                     `json:"schema_version" required:"true"`
	Provider      string                  `json:"provider" required:"true"`
	Version       string                  `json:"version" required:"true"`
	Executable    string                  `json:"executable" required:"true"`
	Identity      pathguard.IdentityToken `json:"identity" required:"true"`
	SHA256        string                  `json:"sha256" required:"true"`
}

func MeasureAgentConfiguration(provider, version, executable string) (AgentConfiguration, error) {
	if provider != "codex" || version == "" || !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return AgentConfiguration{}, errors.New("verified Agent metadata is invalid")
	}
	file, err := os.Open(executable)
	if err != nil {
		return AgentConfiguration{}, errors.New("open verified Agent")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return AgentConfiguration{}, errors.New("verified Agent is not a regular file")
	}
	identity, err := pathguard.PhysicalFileIdentity(file)
	if err != nil {
		return AgentConfiguration{}, errors.New("measure verified Agent identity")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return AgentConfiguration{}, errors.New("measure verified Agent digest")
	}
	return AgentConfiguration{SchemaVersion: 1, Provider: provider, Version: version, Executable: executable, Identity: identity, SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

func SaveAgentConfiguration(dataRoot string, configuration AgentConfiguration) error {
	if err := validateAgentDataRoot(dataRoot); err != nil {
		return err
	}
	if err := validateAgentConfiguration(configuration); err != nil {
		return err
	}
	current, err := MeasureAgentConfiguration(configuration.Provider, configuration.Version, configuration.Executable)
	if err != nil || current != configuration {
		return errors.Join(errors.New("verified Agent changed before configuration"), err)
	}
	body, err := strictjson.Encode(configuration)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dataRoot, agentConfigurationName), body, 0o600)
}

func LoadAgentConfiguration(dataRoot string) (AgentConfiguration, error) {
	if err := validateAgentDataRoot(dataRoot); err != nil {
		return AgentConfiguration{}, err
	}
	path := filepath.Join(dataRoot, agentConfigurationName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return AgentConfiguration{}, errors.Join(errors.New("configured Agent record is unavailable or unsafe"), err)
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 16<<10 {
		return AgentConfiguration{}, errors.Join(errors.New("configured Agent record is unavailable or too large"), err)
	}
	var configuration AgentConfiguration
	if err := strictjson.Decode(body, &configuration); err != nil {
		return AgentConfiguration{}, err
	}
	if err := validateAgentConfiguration(configuration); err != nil {
		return AgentConfiguration{}, err
	}
	current, err := MeasureAgentConfiguration(configuration.Provider, configuration.Version, configuration.Executable)
	if err != nil || current != configuration {
		return AgentConfiguration{}, errors.Join(errors.New("configured Agent executable changed"), err)
	}
	return configuration, nil
}

func validateAgentDataRoot(dataRoot string) error {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return errors.New("Agent configuration requires a clean absolute data root")
	}
	info, err := os.Stat(dataRoot)
	if err != nil || !info.IsDir() {
		return errors.Join(errors.New("Agent configuration data root is unavailable"), err)
	}
	return nil
}

func validateAgentConfiguration(configuration AgentConfiguration) error {
	if configuration.SchemaVersion != 1 || configuration.Provider != "codex" || configuration.Version == "" || !filepath.IsAbs(configuration.Executable) || filepath.Clean(configuration.Executable) != configuration.Executable || !configuration.Identity.Valid() || len(configuration.SHA256) != 64 {
		return errors.New("configured Agent metadata is invalid")
	}
	for _, value := range configuration.SHA256 {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return errors.New("configured Agent digest is invalid")
		}
	}
	return nil
}
