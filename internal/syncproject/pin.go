package syncproject

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	maxPinnedConfigEntries = 4_096
	maxPinnedConfigBytes   = 64 << 20
)

type pinCheckpointStage string

const (
	pinAfterCapture      pinCheckpointStage = "after_capture"
	pinAfterParse        pinCheckpointStage = "after_parse"
	pinAfterMapping      pinCheckpointStage = "after_mapping"
	pinBeforeVaultOpen   pinCheckpointStage = "before_vault_open"
	pinAfterVaultOpen    pinCheckpointStage = "after_vault_open"
	pinBeforeFinalVerify pinCheckpointStage = "before_final_verify"
)

// MappingPin is an opaque, physically authenticated configured mapping. A
// worker can reuse it across packet syncs; callers cannot construct its
// identity/config proof structurally.
type MappingPin struct {
	project  *pathguard.Directory
	vault    *pathguard.Directory
	data     *pathguard.Directory
	syncData *pathguard.Directory
	mapping  config.ProjectMapping
	config   configNamespacePin
}

type configNamespacePin struct {
	entries []configEntryPin
}

type configEntryPin struct {
	name   string
	info   os.FileInfo
	digest [sha256.Size]byte
	body   []byte
	dir    bool
}

// PinMapping resolves configuration and pins Project, Vault, Data, per-project
// sync Data, and the exact configuration namespace used for that resolution.
func PinMapping(options Options) (_ *MappingPin, retErr error) {
	if !filepath.IsAbs(options.DataDir) {
		return nil, errors.New("sync data directory must be absolute")
	}
	data, err := pathguard.Open(options.DataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg, loadErr := config.Load(filepath.Join(options.DataDir, "config.toml"))
			if loadErr != nil {
				return nil, loadErr
			}
			_, project, mappingErr := resolveMapping(cfg, options.ProjectID, options.CWD)
			if project != nil {
				_ = project.Close()
			}
			return nil, mappingErr
		}
		return nil, fmt.Errorf("open sync data directory: %w", err)
	}
	pin := &MappingPin{data: data}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, pin.Close())
		}
	}()
	configPin, err := captureConfigNamespace(data)
	if err != nil {
		return nil, err
	}
	if err := runPinCheckpoint(options, pinAfterCapture); err != nil {
		return nil, err
	}
	cfg, err := config.LoadNamespaceSnapshot(configPin.snapshot())
	if err != nil {
		return nil, err
	}
	if err := runPinCheckpoint(options, pinAfterParse); err != nil {
		return nil, err
	}
	mapping, project, err := resolveMapping(cfg, options.ProjectID, options.CWD)
	if err != nil {
		return nil, err
	}
	pin.mapping, pin.project, pin.config = mapping, project, configPin
	if err := runPinCheckpoint(options, pinAfterMapping); err != nil {
		return nil, err
	}
	if mapping.VaultRoot == "" || mapping.VaultReviewPath == "" || mapping.VaultCaseMode == "" {
		return nil, errors.New("project has no complete Obsidian sync mapping")
	}
	if err := runPinCheckpoint(options, pinBeforeVaultOpen); err != nil {
		return nil, err
	}
	vault, err := pathguard.Open(mapping.VaultRoot)
	if err != nil {
		return nil, errors.New("configured Vault root is unavailable or unsafe")
	}
	pin.vault = vault
	if err := runPinCheckpoint(options, pinAfterVaultOpen); err != nil {
		return nil, err
	}
	syncData, err := pathguard.Open(filepath.Join(data.Path, "projects", mapping.ID))
	if err != nil {
		return nil, errors.New("configured project sync data root is unavailable or unsafe")
	}
	pin.syncData = syncData
	if err := runPinCheckpoint(options, pinBeforeFinalVerify); err != nil {
		return nil, err
	}
	if err := pin.verify(options); err != nil {
		return nil, err
	}
	return pin, nil
}

func runPinCheckpoint(options Options, stage pinCheckpointStage) error {
	if options.pinCheckpoint == nil {
		return nil
	}
	return options.pinCheckpoint(stage)
}

func (pin *MappingPin) Close() error {
	if pin == nil {
		return nil
	}
	return errors.Join(closeDirectory(pin.syncData), closeDirectory(pin.vault), closeDirectory(pin.project), closeDirectory(pin.data))
}

// Recheck authenticates the current namespace against this pin without
// exposing any of its physical handles or captured configuration bytes.
func (pin *MappingPin) Recheck(options Options) error {
	return pin.verify(options)
}

// AuthenticateBinding proves this one captured mapping resolves to the root
// handles the worker already authenticated for its lease lifetime.
func (pin *MappingPin) AuthenticateBinding(projectID string, project, vault, data os.FileInfo) error {
	if pin == nil || pin.project == nil || pin.vault == nil || pin.data == nil ||
		projectID == "" || projectID != pin.mapping.ID || project == nil || vault == nil || data == nil ||
		!os.SameFile(pin.project.Info(), project) || !os.SameFile(pin.vault.Info(), vault) || !os.SameFile(pin.data.Info(), data) {
		return errors.New("sync mapping pin does not match authenticated worker roots")
	}
	return nil
}

func closeDirectory(directory *pathguard.Directory) error {
	if directory == nil {
		return nil
	}
	return directory.Close()
}

func (pin *MappingPin) verify(options Options) error {
	if pin == nil || pin.project == nil || pin.vault == nil || pin.data == nil || pin.syncData == nil || pin.mapping.ID == "" {
		return errors.New("sync mapping pin is unavailable")
	}
	if options.ProjectID != "" && options.ProjectID != pin.mapping.ID {
		return errors.New("sync mapping pin belongs to another project")
	}
	for _, expected := range []struct {
		name      string
		path      string
		directory *pathguard.Directory
	}{
		{name: "Project", path: pin.project.Path, directory: pin.project},
		{name: "Vault", path: pin.vault.Path, directory: pin.vault},
		{name: "Data", path: pin.data.Path, directory: pin.data},
		{name: "project sync Data", path: pin.syncData.Path, directory: pin.syncData},
	} {
		reopened, err := pathguard.Open(expected.path)
		if err != nil {
			return fmt.Errorf("configured %s identity changed", expected.name)
		}
		same := os.SameFile(expected.directory.Info(), reopened.Info())
		closeErr := reopened.Close()
		if closeErr != nil || !same {
			return fmt.Errorf("configured %s identity changed", expected.name)
		}
	}
	if options.CWD != "" {
		requested, err := pathguard.Open(options.CWD)
		if err != nil {
			return errors.New("requested Project root identity changed")
		}
		same := os.SameFile(pin.project.Info(), requested.Info())
		closeErr := requested.Close()
		if closeErr != nil || !same {
			return errors.New("requested Project root identity changed")
		}
	}
	requestedData, err := pathguard.Open(options.DataDir)
	if err != nil {
		return errors.New("requested Data root identity changed")
	}
	sameData := os.SameFile(pin.data.Info(), requestedData.Info())
	closeErr := requestedData.Close()
	if closeErr != nil || !sameData {
		return errors.New("requested Data root identity changed")
	}
	return verifyConfigNamespace(pin.data, pin.config)
}

func captureConfigNamespace(data *pathguard.Directory) (configNamespacePin, error) {
	if data == nil || data.Root == nil {
		return configNamespacePin{}, errors.New("configuration Data root is unavailable")
	}
	var entries []configEntryPin
	for _, name := range []string{"config.toml", atomicfile.BackupPath("config.toml")} {
		entry, found, err := captureConfigFile(data.Root, name)
		if err != nil {
			return configNamespacePin{}, err
		}
		if found {
			entries = append(entries, entry)
		}
	}
	fragmentsInfo, err := data.Root.Lstat(config.ProjectFragmentsDir)
	if err == nil {
		if err := config.ValidateNamespaceSnapshotEntry(data.Path, config.ProjectFragmentsDir, fragmentsInfo); err != nil {
			return configNamespacePin{}, errors.New("project fragment namespace is unsafe")
		}
		entries = append(entries, configEntryPin{name: config.ProjectFragmentsDir, info: fragmentsInfo, dir: true})
		fragments, err := data.Root.OpenRoot(config.ProjectFragmentsDir)
		if err != nil {
			return configNamespacePin{}, err
		}
		directory, err := fragments.Open(".")
		if err != nil {
			_ = fragments.Close()
			return configNamespacePin{}, err
		}
		children, readErr := directory.ReadDir(maxPinnedConfigEntries + 1)
		closeErr := errors.Join(directory.Close(), fragments.Close())
		if errors.Is(readErr, io.EOF) {
			readErr = nil
		}
		if readErr != nil || closeErr != nil || len(children) > maxPinnedConfigEntries {
			return configNamespacePin{}, errors.New("project fragment namespace exceeds its bound")
		}
		for _, child := range children {
			entry, found, err := captureConfigFile(data.Root, filepath.Join(config.ProjectFragmentsDir, child.Name()))
			if err != nil || !found {
				return configNamespacePin{}, errors.New("project fragment namespace changed while pinning")
			}
			entries = append(entries, entry)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return configNamespacePin{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return configNamespacePin{entries: entries}, nil
}

func (pin configNamespacePin) snapshot() config.NamespaceSnapshot {
	snapshot := config.NamespaceSnapshot{}
	for _, entry := range pin.entries {
		if entry.dir {
			continue
		}
		file := config.FileSnapshot{Name: filepath.Base(entry.name), Body: append([]byte(nil), entry.body...)}
		switch entry.name {
		case "config.toml":
			snapshot.Primary = &file
		case atomicfile.BackupPath("config.toml"):
			snapshot.Backup = &file
		default:
			if filepath.Dir(entry.name) == config.ProjectFragmentsDir {
				snapshot.ProjectFragments = append(snapshot.ProjectFragments, file)
			}
		}
	}
	return snapshot
}

func captureConfigFile(root *os.Root, name string) (configEntryPin, bool, error) {
	if filepath.IsAbs(name) || name == "." || strings.HasPrefix(filepath.Clean(name), "..") {
		return configEntryPin{}, false, errors.New("configuration entry name is invalid")
	}
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return configEntryPin{}, false, nil
	}
	if err != nil || config.ValidateNamespaceSnapshotEntry(root.Name(), name, before) != nil || before.Size() < 0 || before.Size() > maxPinnedConfigBytes {
		return configEntryPin{}, false, errors.New("configuration entry is unsafe or oversized")
	}
	file, err := root.Open(name)
	if err != nil {
		return configEntryPin{}, false, err
	}
	body, readErr := io.ReadAll(io.LimitReader(file, maxPinnedConfigBytes+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, nameErr := root.Lstat(name)
	if readErr != nil || statErr != nil || closeErr != nil || nameErr != nil || len(body) > maxPinnedConfigBytes ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) || opened.Size() != int64(len(body)) {
		return configEntryPin{}, false, errors.New("configuration entry changed while pinning")
	}
	return configEntryPin{name: name, info: opened, digest: sha256.Sum256(body), body: append([]byte(nil), body...)}, true, nil
}

func verifyConfigNamespace(data *pathguard.Directory, expected configNamespacePin) error {
	current, err := captureConfigNamespace(data)
	if err != nil || len(current.entries) != len(expected.entries) {
		return errors.New("configured mapping namespace changed")
	}
	for index := range expected.entries {
		want, got := expected.entries[index], current.entries[index]
		if want.name != got.name || want.dir != got.dir || !os.SameFile(want.info, got.info) ||
			(!want.dir && !bytes.Equal(want.digest[:], got.digest[:])) {
			return errors.New("configured mapping namespace changed")
		}
	}
	return nil
}
