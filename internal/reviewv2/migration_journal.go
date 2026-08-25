package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	migrationJournalVersion  = 1
	migrationJournalDir      = "migrations"
	maxMigrationJournalBytes = 4 << 20
)

type migrationJournalFile struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
}

type migrationJournalEntry struct {
	RelativePath string `json:"relative_path"`
	Kind         string `json:"kind"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
}

type migrationJournalRoot struct {
	CanonicalPath string                  `json:"canonical_path"`
	Identity      pathguard.IdentityToken `json:"identity"`
}

type migrationJournal struct {
	Version          int                     `json:"version"`
	ProjectKey       string                  `json:"project_key"`
	ProjectID        string                  `json:"project_id"`
	ManifestSHA256   string                  `json:"manifest_sha256"`
	BackupRelative   string                  `json:"backup_relative"`
	Stage            Stage                   `json:"stage"`
	PlannedAt        time.Time               `json:"planned_at"`
	ProjectRoot      migrationJournalRoot    `json:"project_root"`
	DataRoot         migrationJournalRoot    `json:"data_root"`
	Legacy           []migrationJournalFile  `json:"legacy"`
	Writes           []migrationJournalFile  `json:"writes"`
	VisibleInventory []migrationJournalEntry `json:"visible_inventory"`
}

func migrationProjectKey(projectRoot string) (string, error) {
	absolute, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	digest := sha256.Sum256([]byte(absolute))
	return hex.EncodeToString(digest[:]), nil
}

func migrationJournalRelative(projectKey string) string {
	return filepath.ToSlash(filepath.Join(migrationJournalDir, projectKey+".json"))
}

func encodeMigrationJournal(value migrationJournal) ([]byte, error) {
	if err := validateMigrationJournal(value); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, errors.New("cannot encode migration journal")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxMigrationJournalBytes {
		return nil, errors.New("migration journal exceeds size limit")
	}
	return encoded, nil
}

func decodeMigrationJournal(encoded []byte) (migrationJournal, error) {
	if len(encoded) == 0 || len(encoded) > maxMigrationJournalBytes {
		return migrationJournal{}, errors.New("migration journal is corrupt")
	}
	if err := rejectDuplicateJSONKeys(encoded); err != nil {
		return migrationJournal{}, errors.New("migration journal is corrupt")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := scanExactJSONFields(decoder, reflect.TypeOf(migrationJournal{}), "$", make(map[reflect.Type]map[string]reflect.Type)); err != nil {
		return migrationJournal{}, errors.New("migration journal is corrupt")
	}
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value migrationJournal
	if err := decoder.Decode(&value); err != nil {
		return migrationJournal{}, errors.New("migration journal is corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return migrationJournal{}, errors.New("migration journal is corrupt")
	}
	if err := validateMigrationJournal(value); err != nil {
		return migrationJournal{}, err
	}
	return value, nil
}

func validateMigrationJournal(value migrationJournal) error {
	if value.Version != migrationJournalVersion || !lowerHexSHA256(value.ProjectKey) || !validStableID(value.ProjectID) || !strings.HasPrefix(value.ProjectID, "project-") ||
		!lowerHexSHA256(value.ManifestSHA256) || value.BackupRelative != migrationBackupRelative(value.ManifestSHA256) ||
		value.PlannedAt.IsZero() || value.PlannedAt.Location() != time.UTC || !validMigrationJournalRoot(value.ProjectRoot) || !validMigrationJournalRoot(value.DataRoot) {
		return errors.New("migration journal is corrupt")
	}
	projectKey, err := migrationProjectKey(value.ProjectRoot.CanonicalPath)
	if err != nil || projectKey != value.ProjectKey {
		return errors.New("migration journal is corrupt")
	}
	if _, ok := migrationStageIndex(value.Stage); !ok {
		return errors.New("migration journal is corrupt")
	}
	if err := validateJournalFiles(value.Legacy, false); err != nil {
		return err
	}
	if err := validateJournalFiles(value.Writes, true); err != nil {
		return err
	}
	if len(value.Legacy) == 0 || len(value.Writes) != 3 || len(value.VisibleInventory) == 0 {
		return errors.New("migration journal is corrupt")
	}
	writesByPath := make(map[string]migrationJournalFile, len(value.Writes))
	for _, file := range value.Writes {
		writesByPath[file.RelativePath] = file
	}
	for relative, mode := range map[string]uint32{
		ReviewRelativePath: 0o644, HistoryRelativePath: 0o644, MachineLedgerRelativePath: 0o600,
	} {
		file, found := writesByPath[relative]
		if !found || file.Mode != mode {
			return errors.New("migration journal is corrupt")
		}
	}
	seen := make(map[string]struct{}, len(value.VisibleInventory))
	portableSeen := make(map[string]string, len(value.VisibleInventory))
	inventoryByPath := make(map[string]migrationJournalEntry, len(value.VisibleInventory))
	fileCount := 0
	totalBytes := int64(0)
	if len(value.VisibleInventory) > maxMigrationTreeItems {
		return errors.New("migration journal exceeds recovery inventory budget")
	}
	for _, entry := range value.VisibleInventory {
		if !safeMigrationRelative(entry.RelativePath) || !strings.HasPrefix(entry.RelativePath, migrationReviewRoot+"/") ||
			(entry.Kind != "file" && entry.Kind != "directory") || entry.Mode == 0 {
			return errors.New("migration journal is corrupt")
		}
		portableKey, err := migrationPortableInventoryKey(entry.RelativePath)
		if err != nil {
			return errors.New("migration journal is corrupt")
		}
		if previous, collision := portableSeen[portableKey]; collision && previous != entry.RelativePath {
			return errors.New("migration journal contains case or NFC colliding inventory paths")
		}
		portableSeen[portableKey] = entry.RelativePath
		if entry.Kind == "file" {
			if !strings.HasPrefix(entry.SHA256, "sha256:") || !lowerHexSHA256(strings.TrimPrefix(entry.SHA256, "sha256:")) || entry.Size < 0 {
				return errors.New("migration journal is corrupt")
			}
			fileCount++
			if fileCount > maxMigrationFiles || entry.Size > maxMigrationBytes-totalBytes {
				return errors.New("migration journal exceeds 64 MiB or 4096-file recovery budget")
			}
			totalBytes += entry.Size
		} else if entry.SHA256 != "" || entry.Size != 0 {
			return errors.New("migration journal is corrupt")
		}
		if _, duplicate := seen[entry.RelativePath]; duplicate {
			return errors.New("migration journal contains duplicate inventory paths")
		}
		seen[entry.RelativePath] = struct{}{}
		inventoryByPath[entry.RelativePath] = entry
	}
	for _, legacy := range value.Legacy {
		entry, found := inventoryByPath[legacy.RelativePath]
		if !found || entry.Kind != "file" || entry.SHA256 != legacy.SHA256 || entry.Size != legacy.Size || entry.Mode != legacy.Mode {
			return errors.New("migration journal legacy files do not match inventory")
		}
	}
	_, _, manifestDigest, err := buildBackupManifest(value.ProjectID, value.VisibleInventory)
	if err != nil || manifestDigest != value.ManifestSHA256 {
		return errors.New("migration journal manifest does not match inventory")
	}
	return nil
}

func validMigrationJournalRoot(root migrationJournalRoot) bool {
	return filepath.IsAbs(root.CanonicalPath) && filepath.Clean(root.CanonicalPath) == root.CanonicalPath && root.Identity.Valid()
}

func validateJournalFiles(files []migrationJournalFile, writes bool) error {
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !safeMigrationRelative(file.RelativePath) || !strings.HasPrefix(file.SHA256, "sha256:") ||
			!lowerHexSHA256(strings.TrimPrefix(file.SHA256, "sha256:")) || file.Size < 0 || file.Mode == 0 {
			return errors.New("migration journal is corrupt")
		}
		if writes && file.RelativePath != ReviewRelativePath && file.RelativePath != HistoryRelativePath && file.RelativePath != MachineLedgerRelativePath {
			return errors.New("migration journal is corrupt")
		}
		if _, duplicate := seen[file.RelativePath]; duplicate {
			return errors.New("migration journal contains duplicate file paths")
		}
		seen[file.RelativePath] = struct{}{}
	}
	return nil
}

func lowerHexSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func migrationStageIndex(stage Stage) (int, bool) {
	switch stage {
	case StagePlanned:
		return 0, true
	case StageBackupComplete:
		return 1, true
	case StageV2Written:
		return 2, true
	case StageLegacyMoved:
		return 3, true
	case StageCommitted:
		return 4, true
	default:
		return 0, false
	}
}

func sameMigrationJournalIdentity(left, right migrationJournal) bool {
	left.Stage, right.Stage = "", ""
	return reflect.DeepEqual(left, right)
}

func saveMigrationJournal(data *pathguard.Directory, next migrationJournal) error {
	if data == nil || data.Root == nil {
		return errors.New("migration data root is required")
	}
	if err := ensureMigrationDirectory(data, migrationJournalDir, migrationJournalDir); err != nil {
		return fmt.Errorf("create migration journal directory: %w", err)
	}
	if err := inspectMigrationJournalNamespace(data); err != nil {
		return err
	}
	relative := migrationJournalRelative(next.ProjectKey)
	current, found, err := loadMigrationJournal(data, next.ProjectKey)
	if err != nil {
		return err
	}
	if found {
		if !sameMigrationJournalIdentity(current, next) {
			return errors.New("migration journal does not match the active migration")
		}
		currentStage, _ := migrationStageIndex(current.Stage)
		nextStage, _ := migrationStageIndex(next.Stage)
		if nextStage < currentStage || nextStage > currentStage+1 {
			return errors.New("invalid migration stage transition")
		}
	} else if next.Stage != StagePlanned {
		return errors.New("migration journal must begin at planned")
	}
	encoded, err := encodeMigrationJournal(next)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteRootPrepared(data.Root, filepath.FromSlash(relative), encoded, 0o600, securePrivateMigrationFile); err != nil {
		return fmt.Errorf("persist migration journal: %w", err)
	}
	loaded, found, err := loadMigrationJournal(data, next.ProjectKey)
	if err != nil || !found || !reflect.DeepEqual(loaded, next) {
		return errors.New("migration journal failed post-save verification")
	}
	return nil
}

func loadMigrationJournal(data *pathguard.Directory, projectKey string) (migrationJournal, bool, error) {
	if data == nil || data.Root == nil || !lowerHexSHA256(projectKey) {
		return migrationJournal{}, false, errors.New("migration data root or project key is invalid")
	}
	if err := inspectMigrationJournalNamespace(data); err != nil {
		return migrationJournal{}, false, err
	}
	relative := migrationJournalRelative(projectKey)
	encoded, found, err := data.ReadRegularOptional(relative, maxMigrationJournalBytes)
	if err != nil || !found {
		return migrationJournal{}, found, err
	}
	info, err := data.Root.Lstat(filepath.FromSlash(relative))
	if err != nil || !info.Mode().IsRegular() || !privateMigrationPath(filepath.Join(data.Path, filepath.FromSlash(relative)), fs.FileMode(0o600)) {
		return migrationJournal{}, true, errors.New("migration journal is redirected, invalid, or not private")
	}
	value, err := decodeMigrationJournal(encoded)
	if err != nil {
		return migrationJournal{}, true, err
	}
	if value.ProjectKey != projectKey {
		return migrationJournal{}, true, errors.New("migration journal filename does not certify its project")
	}
	return value, true, nil
}

func inspectMigrationJournalNamespace(data *pathguard.Directory) error {
	directory, expected, err := data.OpenDirectory(migrationJournalDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("migration journal directory is redirected or invalid")
	}
	defer directory.Close()
	if !privateMigrationPath(filepath.Join(data.Path, filepath.FromSlash(migrationJournalDir)), 0o700) {
		return errors.New("migration journal directory is not private")
	}
	file, err := directory.Open(".")
	if err != nil {
		return errors.New("cannot inspect migration journal directory")
	}
	defer file.Close()
	entries := make([]os.DirEntry, 0, 64)
	for {
		batch, readErr := file.ReadDir(256)
		if len(entries)+len(batch) > 10_000 {
			return errors.New("migration journal directory exceeds entry limit")
		}
		entries = append(entries, batch...)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return errors.New("cannot inspect migration journal directory")
		}
	}
	seen := make(map[string]string, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		folded := strings.ToLower(name)
		if previous, collision := seen[folded]; collision && previous != name {
			return errors.New("migration journal names collide case-insensitively")
		}
		seen[folded] = name
		if len(name) != 69 || !strings.HasSuffix(name, ".json") || !lowerHexSHA256(strings.TrimSuffix(name, ".json")) {
			return errors.New("invalid migration journal filename or recovery backup collision")
		}
		opened, _, err := data.OpenRegular(filepath.ToSlash(filepath.Join(migrationJournalDir, name)))
		if err != nil || !privateMigrationPath(filepath.Join(data.Path, filepath.FromSlash(filepath.ToSlash(filepath.Join(migrationJournalDir, name)))), fs.FileMode(0o600)) {
			if opened != nil {
				_ = opened.Close()
			}
			return errors.New("migration journal is redirected, invalid, or not private")
		}
		if err := opened.Close(); err != nil {
			return err
		}
	}
	after, err := directory.Stat(".")
	if err != nil || !os.SameFile(expected, after) {
		return errors.New("migration journal directory changed while inspecting")
	}
	return nil
}

func removeMigrationJournal(data *pathguard.Directory, journal migrationJournal) error {
	encoded, err := encodeMigrationJournal(journal)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if err := data.RemoveRegularIfHashMatches(migrationJournalRelative(journal.ProjectKey), hex.EncodeToString(digest[:])); err != nil {
		return fmt.Errorf("remove committed migration journal: %w", err)
	}
	_, found, err := loadMigrationJournal(data, journal.ProjectKey)
	if err != nil || found {
		return errors.New("committed migration journal remains")
	}
	return nil
}

func sortMigrationJournal(value *migrationJournal) {
	sort.Slice(value.Legacy, func(i, j int) bool { return value.Legacy[i].RelativePath < value.Legacy[j].RelativePath })
	sort.Slice(value.Writes, func(i, j int) bool { return value.Writes[i].RelativePath < value.Writes[j].RelativePath })
	sort.Slice(value.VisibleInventory, func(i, j int) bool {
		return value.VisibleInventory[i].RelativePath < value.VisibleInventory[j].RelativePath
	})
}
