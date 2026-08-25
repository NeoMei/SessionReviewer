package reviewv2

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

const (
	migrationReviewRoot   = "docs/session-review"
	migrationBackupRoot   = migrationReviewRoot + "/.session-reviewer/backups"
	maxMigrationFiles     = 4_096
	maxMigrationBytes     = int64(64 << 20)
	maxMigrationTreeItems = 10_000
)

type Stage string

const (
	StagePlanned        Stage = "planned"
	StageBackupComplete Stage = "backup_complete"
	StageV2Written      Stage = "v2_written"
	StageLegacyMoved    Stage = "legacy_moved"
	StageCommitted      Stage = "committed"
)

var ErrStaleMigration = errors.New("stale_migration")

type MigrationPlan struct {
	ProjectRoot    string
	ProjectInfo    os.FileInfo
	BackupRoot     string
	Legacy         []ledger.SnapshotFile
	Writes         []ledger.PlannedFile
	ManifestSHA256 string

	DataRoot            string
	dataInfo            os.FileInfo
	projectRootIdentity migrationJournalRoot
	dataRootIdentity    migrationJournalRoot
	projectKey          string
	projectID           string
	plannedAt           time.Time
	inventory           []migrationJournalEntry
	report              MigrationReport
}

type MigrationReport struct {
	Required       bool     `json:"required"`
	DryRun         bool     `json:"dry_run"`
	BackupRelative string   `json:"backup_relative,omitempty"`
	Creates        []string `json:"creates"`
	Archives       []string `json:"archives"`
}

type migrationHooks struct {
	afterStage                  func(Stage) error
	beforeV2Publish             func(relative string) error
	afterArchiveDirectory       func(destination string) error
	beforeArchivePublish        func(source, destination string) error
	afterArchivePublish         func(source, destination string) error
	beforeArchiveRetire         func(source, quarantine string) error
	afterArchiveRetire          func(source, quarantine string) error
	beforeArchiveRollbackRetire func(destination, quarantine string) error
}

type backupManifest struct {
	Version   int                  `json:"version"`
	ProjectID string               `json:"project_id"`
	Files     []backupManifestFile `json:"files"`
}

type backupManifestFile struct {
	RelativePath    string `json:"relative_path"`
	SHA256          string `json:"sha256"`
	Size            int64  `json:"size"`
	Mode            uint32 `json:"mode"`
	ObjectRelative  string `json:"object_relative"`
	ArchiveRelative string `json:"archive_relative"`
}

func (plan MigrationPlan) Report() MigrationReport {
	report := plan.report
	report.Creates = append([]string(nil), report.Creates...)
	report.Archives = append([]string(nil), report.Archives...)
	return report
}

func migrationBackupRelative(manifestSHA256 string) string {
	return migrationBackupRoot + "/" + manifestSHA256
}

func PlanMigration(projectRoot string, projectInfo os.FileInfo, dataRoot string, now time.Time) (MigrationPlan, error) {
	if projectInfo == nil || strings.TrimSpace(projectRoot) == "" || strings.TrimSpace(dataRoot) == "" || now.IsZero() {
		return MigrationPlan{}, errors.New("migration project, data root, identity, and time are required")
	}
	project, err := openReviewRoot(projectRoot, projectInfo)
	if err != nil {
		return MigrationPlan{}, err
	}
	defer project.Close()
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("open migration data root: %w", err)
	}
	defer data.Close()
	projectIdentity, err := captureMigrationRoot(project)
	if err != nil {
		return MigrationPlan{}, err
	}
	dataIdentity, err := captureMigrationRoot(data)
	if err != nil {
		return MigrationPlan{}, err
	}
	projectKey, err := migrationProjectKey(project.Path)
	if err != nil {
		return MigrationPlan{}, err
	}
	if _, found, err := loadMigrationJournal(data, projectKey); err != nil {
		return MigrationPlan{}, err
	} else if found {
		return MigrationPlan{}, errors.New("migration recovery is required before planning")
	}
	version, err := detectVersionFromDirectory(project)
	if err != nil {
		return MigrationPlan{}, err
	}
	if version == VersionMixed {
		return MigrationPlan{}, errors.New("mixed legacy and v2 state has no matching migration journal")
	}
	if version == VersionV2 {
		return MigrationPlan{ProjectRoot: projectRoot, ProjectInfo: projectInfo, DataRoot: dataRoot, dataInfo: data.Info(), projectRootIdentity: projectIdentity, dataRootIdentity: dataIdentity, report: MigrationReport{DryRun: true, Creates: []string{}, Archives: []string{}}}, nil
	}
	if version != VersionLegacy {
		return MigrationPlan{}, errors.New("review ledger is not a migratable legacy ledger")
	}
	legacyState, err := ledger.LoadExpected(projectRoot, project.Info())
	if err != nil {
		return MigrationPlan{}, err
	}
	usage, err := ledger.SnapshotUsageExpected(projectRoot, project.Info())
	if err != nil {
		return MigrationPlan{}, err
	}
	inventory, err := scanActiveMigrationInventory(project)
	if err != nil {
		return MigrationPlan{}, err
	}
	if err := validateLegacySnapshotInventory(usage.Files, inventory); err != nil {
		return MigrationPlan{}, err
	}
	projected, err := ProjectLegacy(legacyState)
	if err != nil {
		return MigrationPlan{}, err
	}
	writes, err := Render(projectRoot, projected)
	if err != nil {
		return MigrationPlan{}, err
	}
	manifest, manifestBody, manifestSHA256, err := buildBackupManifest(legacyState.ProjectID, inventory)
	if err != nil {
		return MigrationPlan{}, err
	}
	_ = manifest
	_ = manifestBody
	backupRoot := migrationBackupRelative(manifestSHA256)
	if exists, err := migrationEntryExists(project, backupRoot); err != nil {
		return MigrationPlan{}, err
	} else if exists {
		return MigrationPlan{}, errors.New("migration backup collision")
	}
	creates := make([]string, 0, len(writes.Files))
	for _, file := range writes.Files {
		creates = append(creates, file.RelativePath)
	}
	sort.Strings(creates)
	archives := migrationFilePaths(inventory)
	plan := MigrationPlan{
		ProjectRoot: projectRoot, ProjectInfo: projectInfo, BackupRoot: backupRoot,
		Legacy: append([]ledger.SnapshotFile(nil), usage.Files...), Writes: clonePlannedFiles(writes.Files), ManifestSHA256: manifestSHA256,
		DataRoot: dataRoot, dataInfo: data.Info(), projectRootIdentity: projectIdentity, dataRootIdentity: dataIdentity, projectKey: projectKey, projectID: legacyState.ProjectID, plannedAt: now.UTC(), inventory: append([]migrationJournalEntry(nil), inventory...),
		report: MigrationReport{Required: true, DryRun: true, BackupRelative: backupRoot, Creates: creates, Archives: archives},
	}
	return plan, nil
}

func ApplyMigration(plan MigrationPlan) (MigrationReport, error) {
	if !plan.report.Required {
		report := plan.Report()
		report.DryRun = false
		return report, nil
	}
	if err := applyMigrationWithHook(plan, nil); err != nil {
		return MigrationReport{}, err
	}
	report := plan.Report()
	report.Required = false
	report.DryRun = false
	return report, nil
}

func applyMigrationWithHook(plan MigrationPlan, hook func(Stage) error) error {
	return applyMigrationWithHooks(plan, migrationHooks{afterStage: hook})
}

func applyMigrationWithHooks(plan MigrationPlan, hooks migrationHooks) error {
	journal, err := journalFromMigrationPlan(plan)
	if err != nil {
		return err
	}
	project, data, err := openMigrationRoots(plan.ProjectRoot, plan.ProjectInfo, plan.DataRoot, plan.dataInfo)
	if err != nil {
		return err
	}
	defer project.Close()
	defer data.Close()
	if _, found, err := loadMigrationJournal(data, journal.ProjectKey); err != nil {
		return err
	} else if found {
		return errors.New("migration journal already exists; recover before applying")
	}
	if exists, err := migrationEntryExists(project, plan.BackupRoot); err != nil {
		return err
	} else if exists {
		return errors.New("migration backup collision")
	}
	if err := validateMigrationSources(project, journal, false); err != nil {
		return err
	}
	if err := validateAbsentMigrationV2Preimages(project, journal); err != nil {
		return err
	}
	if err := validateMigrationRootBindings(project, data, journal); err != nil {
		return err
	}
	if err := saveMigrationJournal(data, journal); err != nil {
		return err
	}
	if hooks.afterStage != nil {
		if err := hooks.afterStage(StagePlanned); err != nil {
			return err
		}
	}
	return resumeMigration(project, data, &journal, clonePlannedFiles(plan.Writes), hooks)
}

func RecoverMigration(projectRoot string, projectInfo os.FileInfo, dataRoot string) error {
	if projectInfo == nil {
		return errors.New("expected project root identity is required")
	}
	project, err := openReviewRoot(projectRoot, projectInfo)
	if err != nil {
		return err
	}
	defer project.Close()
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return fmt.Errorf("open migration data root: %w", err)
	}
	defer data.Close()
	projectKey, err := migrationProjectKey(project.Path)
	if err != nil {
		return err
	}
	journal, found, err := loadMigrationJournal(data, projectKey)
	if err != nil {
		return err
	}
	if !found {
		version, detectErr := detectVersionFromDirectory(project)
		if detectErr != nil {
			return detectErr
		}
		if version == VersionMixed {
			return errors.New("mixed legacy and v2 state has no matching migration journal")
		}
		return nil
	}
	writes, err := reconstructMigrationWrites(projectRoot, project.Info(), journal)
	if err != nil {
		return err
	}
	return resumeMigration(project, data, &journal, writes, migrationHooks{})
}

func openMigrationRoots(projectRoot string, projectInfo os.FileInfo, dataRoot string, dataInfo os.FileInfo) (*pathguard.Directory, *pathguard.Directory, error) {
	project, err := openReviewRoot(projectRoot, projectInfo)
	if err != nil {
		return nil, nil, err
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		_ = project.Close()
		return nil, nil, fmt.Errorf("open migration data root: %w", err)
	}
	if dataInfo != nil && !os.SameFile(dataInfo, data.Info()) {
		_ = data.Close()
		_ = project.Close()
		return nil, nil, errors.New("opened migration data root does not match planned identity")
	}
	return project, data, nil
}

func journalFromMigrationPlan(plan MigrationPlan) (migrationJournal, error) {
	projectKey := plan.projectKey
	if !lowerHexSHA256(projectKey) {
		var err error
		projectKey, err = migrationProjectKey(plan.ProjectRoot)
		if err != nil {
			return migrationJournal{}, err
		}
	}
	journal := migrationJournal{
		Version: migrationJournalVersion, ProjectKey: projectKey, ProjectID: plan.projectID,
		ManifestSHA256: plan.ManifestSHA256, BackupRelative: plan.BackupRoot, Stage: StagePlanned, PlannedAt: plan.plannedAt,
		ProjectRoot: plan.projectRootIdentity, DataRoot: plan.dataRootIdentity,
		Legacy: snapshotJournalFiles(plan.Legacy), Writes: plannedJournalFiles(plan.Writes), VisibleInventory: append([]migrationJournalEntry(nil), plan.inventory...),
	}
	sortMigrationJournal(&journal)
	if err := validateMigrationJournal(journal); err != nil {
		return migrationJournal{}, err
	}
	return journal, nil
}

func resumeMigration(project, data *pathguard.Directory, journal *migrationJournal, writes []ledger.PlannedFile, hooks migrationHooks) error {
	if project == nil || data == nil || journal == nil {
		return errors.New("migration recovery state is required")
	}
	if journal.Stage == StagePlanned {
		if err := validateMigrationRootBindings(project, data, *journal); err != nil {
			return err
		}
		if err := validateMigrationSources(project, *journal, false); err != nil {
			return err
		}
		if err := createAndVerifyMigrationBackup(project, *journal); err != nil {
			return err
		}
		if err := advanceMigrationJournal(data, journal, StageBackupComplete); err != nil {
			return err
		}
		if hooks.afterStage != nil {
			if err := hooks.afterStage(StageBackupComplete); err != nil {
				return err
			}
		}
	}
	if journal.Stage == StageBackupComplete {
		if err := validateMigrationRootBindings(project, data, *journal); err != nil {
			return err
		}
		if err := validateMigrationSources(project, *journal, false); err != nil {
			return err
		}
		if err := verifyMigrationBackup(project, *journal, false); err != nil {
			return err
		}
		if len(writes) != 3 {
			return staleMigration("cannot reconstruct planned v2 documents")
		}
		if err := writeMigrationV2(project, *journal, writes, hooks); err != nil {
			return err
		}
		if err := validateMigrationSources(project, *journal, false); err != nil {
			return err
		}
		if err := advanceMigrationJournal(data, journal, StageV2Written); err != nil {
			return err
		}
		if hooks.afterStage != nil {
			if err := hooks.afterStage(StageV2Written); err != nil {
				return err
			}
		}
	}
	if journal.Stage == StageV2Written {
		if err := validateMigrationRootBindings(project, data, *journal); err != nil {
			return err
		}
		if err := validateMigrationV2(project, *journal); err != nil {
			return err
		}
		if err := validateMigrationSources(project, *journal, true); err != nil {
			return err
		}
		if err := verifyMigrationBackup(project, *journal, false); err != nil {
			return err
		}
		if err := archiveMigrationSources(project, *journal, hooks); err != nil {
			return err
		}
		if err := advanceMigrationJournal(data, journal, StageLegacyMoved); err != nil {
			return err
		}
		if hooks.afterStage != nil {
			if err := hooks.afterStage(StageLegacyMoved); err != nil {
				return err
			}
		}
	}
	if journal.Stage == StageLegacyMoved {
		if err := validateMigrationRootBindings(project, data, *journal); err != nil {
			return err
		}
		if err := verifyCommittedMigration(project, *journal); err != nil {
			return err
		}
		if err := advanceMigrationJournal(data, journal, StageCommitted); err != nil {
			return err
		}
		if hooks.afterStage != nil {
			if err := hooks.afterStage(StageCommitted); err != nil {
				return err
			}
		}
	}
	if journal.Stage != StageCommitted {
		return errors.New("migration did not reach committed stage")
	}
	if err := validateMigrationRootBindings(project, data, *journal); err != nil {
		return err
	}
	if err := verifyCommittedMigration(project, *journal); err != nil {
		return err
	}
	return removeMigrationJournal(data, *journal)
}

func captureMigrationRoot(directory *pathguard.Directory) (migrationJournalRoot, error) {
	if directory == nil || directory.Root == nil || !filepath.IsAbs(directory.Path) {
		return migrationJournalRoot{}, errors.New("migration root is required")
	}
	identity, err := directory.PhysicalIdentity()
	if err != nil {
		return migrationJournalRoot{}, fmt.Errorf("capture migration root identity: %w", err)
	}
	return migrationJournalRoot{CanonicalPath: directory.Path, Identity: identity}, nil
}

func validateMigrationRootBindings(project, data *pathguard.Directory, journal migrationJournal) error {
	for _, item := range []struct {
		name      string
		directory *pathguard.Directory
		expected  migrationJournalRoot
	}{
		{name: "project", directory: project, expected: journal.ProjectRoot},
		{name: "data", directory: data, expected: journal.DataRoot},
	} {
		current, err := captureMigrationRoot(item.directory)
		if err != nil || current != item.expected {
			return staleMigration("physical " + item.name + " root identity changed")
		}
		reopened, err := pathguard.Open(item.expected.CanonicalPath)
		if err != nil {
			return staleMigration("physical " + item.name + " root identity changed")
		}
		reopenedIdentity, identityErr := captureMigrationRoot(reopened)
		closeErr := reopened.Close()
		if identityErr != nil || closeErr != nil || reopenedIdentity != item.expected {
			return staleMigration("physical " + item.name + " root identity changed")
		}
	}
	return nil
}

func advanceMigrationJournal(data *pathguard.Directory, journal *migrationJournal, stage Stage) error {
	next := *journal
	next.Stage = stage
	if err := saveMigrationJournal(data, next); err != nil {
		return err
	}
	*journal = next
	return nil
}

func reconstructMigrationWrites(projectRoot string, projectInfo os.FileInfo, journal migrationJournal) ([]ledger.PlannedFile, error) {
	stage, _ := migrationStageIndex(journal.Stage)
	if stage > 1 {
		return nil, nil
	}
	legacyState, err := ledger.LoadExpected(projectRoot, projectInfo)
	if err != nil {
		return nil, staleMigration("legacy state changed after migration planning")
	}
	if !equalSnapshotJournal(ledger.SnapshotFiles(legacyState), journal.Legacy) {
		return nil, staleMigration("legacy state changed after migration planning")
	}
	projected, err := ProjectLegacy(legacyState)
	if err != nil {
		return nil, staleMigration("legacy state no longer projects to v2")
	}
	plan, err := Render(projectRoot, projected)
	if err != nil || !equalPlannedJournal(plan.Files, journal.Writes) {
		return nil, staleMigration("planned v2 documents no longer match the journal")
	}
	return clonePlannedFiles(plan.Files), nil
}

func validateMigrationSources(project *pathguard.Directory, journal migrationJournal, allowArchived bool) error {
	active, err := scanActiveMigrationInventory(project)
	if err != nil {
		return staleMigration(err.Error())
	}
	archived := []migrationJournalEntry(nil)
	archiveRoot := journal.BackupRelative + "/archive"
	if exists, err := migrationEntryExists(project, archiveRoot); err != nil {
		return err
	} else if exists {
		archived, err = scanMigrationInventory(project, archiveRoot, migrationReviewRoot, false)
		if err != nil {
			return staleMigration(err.Error())
		}
	}
	if !allowArchived && len(archived) != 0 {
		return staleMigration("legacy archive advanced beyond the journal stage")
	}
	combined, err := combineMigrationLocations(project, journal, active, archived, allowArchived)
	if err != nil {
		return err
	}
	sort.Slice(combined, func(i, j int) bool { return combined[i].RelativePath < combined[j].RelativePath })
	if !equalMigrationInventory(combined, journal.VisibleInventory) {
		return staleMigration("legacy files were added, removed, modified, or moved after planning")
	}
	return nil
}

func combineMigrationLocations(project *pathguard.Directory, journal migrationJournal, active, archived []migrationJournalEntry, allowAliases bool) ([]migrationJournalEntry, error) {
	combined := make(map[string]migrationJournalEntry, len(active)+len(archived))
	for _, entry := range active {
		combined[entry.RelativePath] = entry
	}
	for _, entry := range archived {
		if previous, duplicate := combined[entry.RelativePath]; duplicate {
			if !allowAliases || previous != entry {
				return nil, staleMigration("migration source location is ambiguous or unsafe")
			}
			if entry.Kind == "directory" {
				continue
			}
			activeInfo, activeErr := project.Root.Lstat(filepath.FromSlash(entry.RelativePath))
			archiveRelative := journal.BackupRelative + "/archive/" + strings.TrimPrefix(entry.RelativePath, migrationReviewRoot+"/")
			archiveInfo, archiveErr := project.Root.Lstat(filepath.FromSlash(archiveRelative))
			if activeErr != nil || archiveErr != nil || !os.SameFile(activeInfo, archiveInfo) {
				return nil, staleMigration("migration source location is ambiguous or unsafe")
			}
			continue
		}
		combined[entry.RelativePath] = entry
	}
	result := make([]migrationJournalEntry, 0, len(combined))
	for _, entry := range combined {
		result = append(result, entry)
	}
	return result, nil
}

func scanActiveMigrationInventory(project *pathguard.Directory) ([]migrationJournalEntry, error) {
	return scanMigrationInventory(project, migrationReviewRoot, migrationReviewRoot, true)
}

func scanMigrationInventory(project *pathguard.Directory, scanRoot, mappedRoot string, excludeV2 bool) ([]migrationJournalEntry, error) {
	opened, _, err := project.OpenDirectory(scanRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("migration tree root is redirected or invalid")
	}
	_ = opened.Close()
	entries := make([]migrationJournalEntry, 0)
	seenKeys := make(map[string]string)
	fileCount := 0
	totalBytes := int64(0)
	itemCount := 0
	var walk func(actual, mapped string) error
	walk = func(actual, mapped string) error {
		root, expected, err := project.OpenDirectory(actual)
		if err != nil {
			return errors.New("migration directory is redirected or invalid")
		}
		defer root.Close()
		file, err := root.Open(".")
		if err != nil {
			return errors.New("cannot enumerate migration directory")
		}
		var children []os.DirEntry
		for {
			batch, readErr := file.ReadDir(256)
			if len(children)+len(batch) > maxMigrationTreeItems {
				_ = file.Close()
				return errors.New("migration tree exceeds entry limit")
			}
			children = append(children, batch...)
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = file.Close()
				return errors.New("cannot enumerate migration directory")
			}
		}
		if err := file.Close(); err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			name := child.Name()
			if atomicfile.IsRootDirectoryTemporaryName(name) {
				return errors.New("migration tree contains incomplete machine directory staging")
			}
			if actual == migrationReviewRoot && name == ".session-reviewer" {
				continue
			}
			mappedRelative := path.Join(mapped, name)
			if excludeV2 && (mappedRelative == ReviewRelativePath || mappedRelative == HistoryRelativePath) {
				continue
			}
			if !safeMigrationRelative(mappedRelative) {
				return fmt.Errorf("unsafe legacy path %q", mappedRelative)
			}
			key, err := migrationPortableInventoryKey(mappedRelative)
			if err != nil {
				return fmt.Errorf("unsafe legacy path %q", mappedRelative)
			}
			if previous, collision := seenKeys[key]; collision && previous != mappedRelative {
				return fmt.Errorf("legacy paths collide by case or NFC: %q and %q", previous, mappedRelative)
			}
			seenKeys[key] = mappedRelative
			actualRelative := path.Join(actual, name)
			info, err := project.Root.Lstat(filepath.FromSlash(actualRelative))
			if err != nil {
				return errors.New("migration tree changed while scanning")
			}
			itemCount++
			if itemCount > maxMigrationTreeItems {
				return errors.New("migration tree exceeds entry limit")
			}
			if info.IsDir() {
				childRoot, _, err := project.OpenDirectory(actualRelative)
				if err != nil {
					return errors.New("migration directory is a symlink, junction, or reparse point")
				}
				_ = childRoot.Close()
				entries = append(entries, migrationJournalEntry{RelativePath: mappedRelative, Kind: "directory", Mode: uint32(info.Mode().Perm())})
				if err := walk(actualRelative, mappedRelative); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return errors.New("migration tree contains a symlink, junction, reparse point, or special entry")
			}
			fileCount++
			if fileCount > maxMigrationFiles || info.Size() < 0 || info.Size() > maxMigrationBytes-totalBytes {
				return errors.New("migration backup exceeds 64 MiB or 4096 files")
			}
			body, found, err := project.ReadRegular(actualRelative, maxMigrationBytes-totalBytes)
			if err != nil || !found {
				return errors.New("migration file changed while scanning")
			}
			totalBytes += int64(len(body))
			digest := sha256.Sum256(body)
			entries = append(entries, migrationJournalEntry{RelativePath: mappedRelative, Kind: "file", SHA256: "sha256:" + hex.EncodeToString(digest[:]), Size: int64(len(body)), Mode: uint32(info.Mode().Perm())})
		}
		after, err := root.Stat(".")
		if err != nil || !os.SameFile(expected, after) {
			return errors.New("migration directory changed while scanning")
		}
		return nil
	}
	if err := walk(scanRoot, mappedRoot); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelativePath < entries[j].RelativePath })
	return entries, nil
}

func validateLegacySnapshotInventory(files []ledger.SnapshotFile, inventory []migrationJournalEntry) error {
	byPath := make(map[string]migrationJournalEntry, len(inventory))
	for _, entry := range inventory {
		byPath[entry.RelativePath] = entry
	}
	for _, file := range files {
		entry, found := byPath[file.RelativePath]
		if !found || entry.Kind != "file" || entry.SHA256 != file.SHA256 || entry.Size != file.Size || fs.FileMode(entry.Mode).Perm() != file.Perm.Perm() {
			return errors.New("legacy ledger snapshot does not match the migration inventory")
		}
	}
	return nil
}

func buildBackupManifest(projectID string, inventory []migrationJournalEntry) (backupManifest, []byte, string, error) {
	manifest := backupManifest{Version: 1, ProjectID: projectID, Files: make([]backupManifestFile, 0)}
	for _, entry := range inventory {
		if entry.Kind != "file" {
			continue
		}
		digest := strings.TrimPrefix(entry.SHA256, "sha256:")
		relative := strings.TrimPrefix(entry.RelativePath, migrationReviewRoot+"/")
		manifest.Files = append(manifest.Files, backupManifestFile{
			RelativePath: entry.RelativePath, SHA256: entry.SHA256, Size: entry.Size, Mode: entry.Mode,
			ObjectRelative: "objects/" + digest, ArchiveRelative: "archive/" + relative,
		})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].RelativePath < manifest.Files[j].RelativePath })
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return backupManifest{}, nil, "", err
	}
	body = append(body, '\n')
	digest := sha256.Sum256(body)
	return manifest, body, hex.EncodeToString(digest[:]), nil
}

func createAndVerifyMigrationBackup(project *pathguard.Directory, journal migrationJournal) error {
	manifest, manifestBody, digest, err := buildBackupManifest(journal.ProjectID, journal.VisibleInventory)
	if err != nil || digest != journal.ManifestSHA256 {
		return errors.New("migration backup manifest does not match journal")
	}
	for _, relative := range []string{
		migrationReviewRoot + "/.session-reviewer",
		migrationBackupRoot,
		journal.BackupRelative,
		journal.BackupRelative + "/objects",
	} {
		if err := ensureMigrationDirectory(project, relative, migrationReviewRoot+"/.session-reviewer"); err != nil {
			return fmt.Errorf("create migration backup tree: %w", err)
		}
	}
	if err := scanExactMigrationBackup(project, journal, manifest, manifestBody, false, false); err != nil {
		return err
	}
	objects, _, err := project.OpenDirectory(journal.BackupRelative + "/objects")
	if err != nil {
		return err
	}
	defer objects.Close()
	for _, item := range manifest.Files {
		entry := migrationInventoryByPath(journal.VisibleInventory)[item.RelativePath]
		body, err := readMigrationSource(project, journal, entry)
		if err != nil {
			return err
		}
		if err := writeRootCreateIfAbsent(objects, strings.TrimPrefix(item.ObjectRelative, "objects/"), body, 0o600); err != nil {
			return fmt.Errorf("create content-addressed migration backup: %w", err)
		}
	}
	backup, _, err := project.OpenDirectory(journal.BackupRelative)
	if err != nil {
		return err
	}
	defer backup.Close()
	if err := writeRootCreateIfAbsent(backup, "manifest.json", manifestBody, 0o600); err != nil {
		return fmt.Errorf("create migration backup manifest: %w", err)
	}
	return verifyMigrationBackup(project, journal, false)
}

func verifyMigrationBackup(project *pathguard.Directory, journal migrationJournal, requireArchive bool) error {
	manifest, manifestBody, digest, err := buildBackupManifest(journal.ProjectID, journal.VisibleInventory)
	if err != nil || digest != journal.ManifestSHA256 {
		return errors.New("migration backup manifest does not match journal")
	}
	if err := scanExactMigrationBackup(project, journal, manifest, manifestBody, requireArchive, true); err != nil {
		return err
	}
	for _, relative := range []string{
		migrationReviewRoot + "/.session-reviewer",
		migrationBackupRoot,
		journal.BackupRelative,
		journal.BackupRelative + "/objects",
	} {
		if err := requireMigrationDirectoryMode(project, relative, 0o700); err != nil {
			return errors.New("migration backup directory is redirected, changed, or not private")
		}
	}
	if requireArchive {
		if err := requireMigrationDirectoryMode(project, journal.BackupRelative+"/archive", 0o700); err != nil {
			return errors.New("migration archive directory is redirected, changed, or not private")
		}
		if err := requireMigrationDirectoryMode(project, journal.BackupRelative+"/quarantine", 0o700); err != nil {
			return errors.New("migration quarantine directory is redirected, changed, or not private")
		}
	}
	body, found, err := project.ReadRegularOptional(journal.BackupRelative+"/manifest.json", int64(len(manifestBody)))
	if err != nil || !found || !bytes.Equal(body, manifestBody) {
		return errors.New("migration backup manifest is missing or changed")
	}
	if err := requireMigrationFileMode(project, journal.BackupRelative+"/manifest.json", 0o600); err != nil {
		return errors.New("migration backup manifest is redirected, invalid, or not private")
	}
	for _, file := range manifest.Files {
		objectRelative := journal.BackupRelative + "/" + file.ObjectRelative
		object, found, err := project.ReadRegularOptional(objectRelative, file.Size)
		if err != nil || !found || int64(len(object)) != file.Size || hashPrefixed(object) != file.SHA256 {
			return errors.New("migration backup object is missing or changed")
		}
		if err := requireMigrationFileMode(project, objectRelative, 0o600); err != nil {
			return errors.New("migration backup object is redirected, invalid, or not private")
		}
		if requireArchive {
			archived, found, err := project.ReadRegularOptional(journal.BackupRelative+"/"+file.ArchiveRelative, file.Size)
			if err != nil || !found || int64(len(archived)) != file.Size || hashPrefixed(archived) != file.SHA256 {
				return errors.New("migration archive is missing or changed")
			}
			entry := migrationInventoryByPath(journal.VisibleInventory)[file.RelativePath]
			retired, found, err := project.ReadRegularOptional(migrationQuarantinePath(journal, entry, "retired"), file.Size)
			if err != nil || !found || int64(len(retired)) != file.Size || hashPrefixed(retired) != file.SHA256 {
				return errors.New("migration retirement quarantine is missing or changed")
			}
		}
	}
	return nil
}

type expectedMigrationBackupEntry struct {
	kind   string
	hash   string
	size   int64
	mode   fs.FileMode
	needed bool
}

func scanExactMigrationBackup(project *pathguard.Directory, journal migrationJournal, manifest backupManifest, manifestBody []byte, requireArchive, requireBase bool) error {
	expected := map[string]expectedMigrationBackupEntry{
		"manifest.json": {kind: "file", hash: hashPrefixed(manifestBody), size: int64(len(manifestBody)), mode: 0o600, needed: requireBase},
		"objects":       {kind: "directory", mode: 0o700, needed: requireBase},
	}
	for _, file := range manifest.Files {
		expected[file.ObjectRelative] = expectedMigrationBackupEntry{kind: "file", hash: file.SHA256, size: file.Size, mode: 0o600, needed: requireBase}
	}
	archiveAllowed := journal.Stage == StageV2Written || requireArchive
	if archiveAllowed {
		expected["archive"] = expectedMigrationBackupEntry{kind: "directory", mode: 0o700, needed: requireArchive}
		expected["quarantine"] = expectedMigrationBackupEntry{kind: "directory", mode: 0o700, needed: requireArchive}
		for _, entry := range journal.VisibleInventory {
			relative := "archive/" + strings.TrimPrefix(entry.RelativePath, migrationReviewRoot+"/")
			expected[relative] = expectedMigrationBackupEntry{
				kind: entry.Kind, hash: entry.SHA256, size: entry.Size, mode: fs.FileMode(entry.Mode), needed: requireArchive,
			}
			if entry.Kind == "file" {
				retired := strings.TrimPrefix(migrationQuarantinePath(journal, entry, "retired"), journal.BackupRelative+"/")
				expected[retired] = expectedMigrationBackupEntry{kind: "file", hash: entry.SHA256, size: entry.Size, mode: fs.FileMode(entry.Mode), needed: requireArchive}
				rollback := strings.TrimPrefix(migrationQuarantinePath(journal, entry, "rollback"), journal.BackupRelative+"/")
				expected[rollback] = expectedMigrationBackupEntry{kind: "file", hash: entry.SHA256, size: entry.Size, mode: fs.FileMode(entry.Mode), needed: false}
			}
		}
	}
	root, _, err := project.OpenDirectory(journal.BackupRelative)
	if err != nil {
		return errors.New("migration backup root is redirected or invalid")
	}
	_ = root.Close()
	seen := make(map[string]struct{}, len(expected))
	portable := make(map[string]string, len(expected))
	itemCount := 0
	fileCount := 0
	totalBytes := int64(0)
	var walk func(string) error
	walk = func(relative string) error {
		actualRoot := journal.BackupRelative
		if relative != "" {
			actualRoot += "/" + relative
		}
		directory, before, err := project.OpenDirectory(actualRoot)
		if err != nil {
			return errors.New("migration backup directory is redirected or invalid")
		}
		defer directory.Close()
		file, err := directory.Open(".")
		if err != nil {
			return errors.New("cannot enumerate migration backup")
		}
		children := make([]os.DirEntry, 0, 64)
		for {
			batch, readErr := file.ReadDir(256)
			if len(children)+len(batch) > maxMigrationTreeItems-itemCount {
				_ = file.Close()
				return errors.New("migration backup exceeds entry limit")
			}
			children = append(children, batch...)
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = file.Close()
				return errors.New("cannot enumerate migration backup")
			}
		}
		if err := file.Close(); err != nil {
			return errors.New("cannot enumerate migration backup")
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			itemCount++
			if atomicfile.IsRootDirectoryTemporaryName(child.Name()) {
				return errors.New("migration backup contains incomplete directory staging")
			}
			candidate := path.Join(relative, child.Name())
			if !safeMigrationRelative(migrationReviewRoot + "/" + candidate) {
				return errors.New("migration backup contains an unsafe path")
			}
			key, err := platform.PathKey("windows", platform.CaseInsensitive, candidate)
			if err != nil {
				return errors.New("migration backup contains an unsafe portable path")
			}
			if previous, collision := portable[key]; collision && previous != candidate {
				return errors.New("migration backup paths collide by case or NFC")
			}
			portable[key] = candidate
			want, allowed := expected[candidate]
			if !allowed {
				return errors.New("migration backup contains an unexpected entry")
			}
			info, err := project.Root.Lstat(filepath.FromSlash(journal.BackupRelative + "/" + candidate))
			if err != nil {
				return errors.New("migration backup changed while scanning")
			}
			if want.kind == "directory" {
				if !info.IsDir() || !privateMigrationPath(filepath.Join(project.Path, filepath.FromSlash(journal.BackupRelative+"/"+candidate)), want.mode) {
					return errors.New("migration backup directory type or mode changed")
				}
				seen[candidate] = struct{}{}
				if err := walk(candidate); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() || !privateMigrationPath(filepath.Join(project.Path, filepath.FromSlash(journal.BackupRelative+"/"+candidate)), want.mode) || info.Size() != want.size {
				if strings.HasPrefix(candidate, "objects/") {
					return errors.New("migration backup object type, size, or mode changed")
				}
				return errors.New("migration backup file type, size, or mode changed")
			}
			if !strings.HasPrefix(candidate, "quarantine/") {
				fileCount++
				if fileCount > 2*maxMigrationFiles+1 || info.Size() < 0 || info.Size() > 2*maxMigrationBytes+maxMigrationJournalBytes-totalBytes {
					return errors.New("migration backup exceeds bounded namespace budget")
				}
				totalBytes += info.Size()
			}
			body, found, err := project.ReadRegular(journal.BackupRelative+"/"+candidate, want.size)
			if err != nil || !found || int64(len(body)) != want.size || hashPrefixed(body) != want.hash {
				if strings.HasPrefix(candidate, "objects/") {
					return errors.New("migration backup object content changed")
				}
				return errors.New("migration backup file content changed")
			}
			seen[candidate] = struct{}{}
		}
		after, err := directory.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			return errors.New("migration backup directory changed while scanning")
		}
		return nil
	}
	if err := walk(""); err != nil {
		return err
	}
	for relative, want := range expected {
		if want.needed {
			if _, found := seen[relative]; !found {
				return errors.New("migration backup namespace is incomplete")
			}
		}
	}
	return nil
}

func requireMigrationDirectoryMode(project *pathguard.Directory, relative string, mode fs.FileMode) error {
	root, expected, err := project.OpenDirectory(relative)
	if err != nil {
		return err
	}
	after, inspectErr := root.Stat(".")
	closeErr := root.Close()
	if inspectErr != nil || closeErr != nil || !os.SameFile(expected, after) || !privateMigrationPath(filepath.Join(project.Path, filepath.FromSlash(relative)), mode) {
		return errors.New("migration directory identity or mode changed")
	}
	return nil
}

func requireMigrationFileMode(project *pathguard.Directory, relative string, mode fs.FileMode) error {
	file, _, err := project.OpenRegular(relative)
	if err != nil {
		return err
	}
	closeErr := file.Close()
	if closeErr != nil || !privateMigrationPath(filepath.Join(project.Path, filepath.FromSlash(relative)), mode) {
		return errors.New("migration file mode does not match")
	}
	return nil
}

func readMigrationSource(project *pathguard.Directory, journal migrationJournal, entry migrationJournalEntry) ([]byte, error) {
	active := entry.RelativePath
	archive := journal.BackupRelative + "/archive/" + strings.TrimPrefix(entry.RelativePath, migrationReviewRoot+"/")
	activeBody, activeFound, activeErr := project.ReadRegularOptional(active, entry.Size)
	archiveBody, archiveFound, archiveErr := project.ReadRegularOptional(archive, entry.Size)
	if activeErr != nil || archiveErr != nil || activeFound == archiveFound {
		return nil, staleMigration("migration source location is ambiguous or unsafe")
	}
	body := activeBody
	if archiveFound {
		body = archiveBody
	}
	if int64(len(body)) != entry.Size || hashPrefixed(body) != entry.SHA256 {
		return nil, staleMigration("migration source content changed")
	}
	return body, nil
}

func writeMigrationV2(project *pathguard.Directory, journal migrationJournal, writes []ledger.PlannedFile, hooks migrationHooks) error {
	byPath := make(map[string]ledger.PlannedFile, len(writes))
	for _, file := range writes {
		byPath[file.RelativePath] = file
	}
	for _, expected := range journal.Writes {
		file, found := byPath[expected.RelativePath]
		if !found || int64(len(file.Data)) != expected.Size || hashPrefixed(file.Data) != expected.SHA256 || uint32(file.Perm.Perm()) != expected.Mode {
			return staleMigration("planned v2 bytes do not match the migration journal")
		}
		current, exists, err := project.ReadRegularOptional(file.RelativePath, expected.Size)
		if err != nil {
			return err
		}
		if exists && (int64(len(current)) != expected.Size || hashPrefixed(current) != expected.SHA256) {
			return staleMigration("a v2 target was modified during migration")
		}
		if exists {
			if err := requireMigrationFileMode(project, file.RelativePath, fs.FileMode(expected.Mode)); err != nil {
				return staleMigration("a v2 target mode changed during migration")
			}
		}
	}
	for _, expected := range journal.Writes {
		file := byPath[expected.RelativePath]
		_, exists, err := project.ReadRegularOptional(file.RelativePath, expected.Size)
		if err != nil {
			return staleMigration("a v2 target changed before atomic publication")
		}
		if exists {
			continue
		}
		parent, _, err := project.OpenDirectory(path.Dir(file.RelativePath))
		if err != nil {
			return err
		}
		prepare := func(created *os.File) error {
			if file.Perm.Perm() == 0o600 {
				return securePrivateMigrationFile(created)
			}
			return nil
		}
		publishErr := atomicfile.WriteRootFileCreateIfAbsentPrepared(parent, path.Base(file.RelativePath), file.Data, file.Perm.Perm(), prepare, func() error {
			if hooks.beforeV2Publish != nil {
				return hooks.beforeV2Publish(file.RelativePath)
			}
			return nil
		})
		closeErr := parent.Close()
		if publishErr != nil {
			return staleMigration("a v2 target was created or redirected before atomic publication")
		}
		if closeErr != nil {
			return closeErr
		}
		body, found, err := project.ReadRegular(file.RelativePath, expected.Size)
		if err != nil || !found || hashPrefixed(body) != expected.SHA256 {
			return errors.New("v2 migration target failed post-write verification")
		}
	}
	return validateMigrationV2(project, journal)
}

func validateAbsentMigrationV2Preimages(project *pathguard.Directory, journal migrationJournal) error {
	for _, expected := range journal.Writes {
		_, found, err := project.ReadRegularOptional(expected.RelativePath, expected.Size)
		if err != nil || found {
			return staleMigration("a v2 target was created or redirected after migration planning")
		}
	}
	return nil
}

func validateMigrationV2(project *pathguard.Directory, journal migrationJournal) error {
	for _, expected := range journal.Writes {
		body, found, err := project.ReadRegularOptional(expected.RelativePath, expected.Size)
		if err != nil || !found || int64(len(body)) != expected.Size || hashPrefixed(body) != expected.SHA256 {
			return staleMigration("v2 migration target is missing or was edited")
		}
		if err := requireMigrationFileMode(project, expected.RelativePath, fs.FileMode(expected.Mode)); err != nil {
			return staleMigration("v2 migration target mode changed")
		}
	}
	return nil
}

func archiveMigrationSources(project *pathguard.Directory, journal migrationJournal, hooks migrationHooks) error {
	if err := ensureMigrationDirectory(project, journal.BackupRelative+"/archive", migrationReviewRoot+"/.session-reviewer"); err != nil {
		return err
	}
	if err := ensureMigrationDirectory(project, journal.BackupRelative+"/quarantine", migrationReviewRoot+"/.session-reviewer"); err != nil {
		return err
	}
	if err := validateMigrationSources(project, journal, true); err != nil {
		return err
	}
	entries := append([]migrationJournalEntry(nil), journal.VisibleInventory...)
	sort.Slice(entries, func(i, j int) bool {
		leftDepth := strings.Count(entries[i].RelativePath, "/")
		rightDepth := strings.Count(entries[j].RelativePath, "/")
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == "directory"
		}
		if entries[i].Kind == "directory" && leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return entries[i].RelativePath < entries[j].RelativePath
	})
	for _, entry := range entries {
		if entry.Kind != "directory" {
			continue
		}
		destination := journal.BackupRelative + "/archive/" + strings.TrimPrefix(entry.RelativePath, migrationReviewRoot+"/")
		if err := ensureArchiveInventoryDirectory(project, destination, fs.FileMode(entry.Mode)); err != nil {
			return err
		}
		if hooks.afterArchiveDirectory != nil {
			if err := hooks.afterArchiveDirectory(destination); err != nil {
				return err
			}
		}
	}
	for _, entry := range entries {
		if entry.Kind != "file" {
			continue
		}
		if err := archiveMigrationFile(project, journal, entry, hooks); err != nil {
			return err
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		leftDepth := strings.Count(entries[i].RelativePath, "/")
		rightDepth := strings.Count(entries[j].RelativePath, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return entries[i].RelativePath > entries[j].RelativePath
	})
	for _, entry := range entries {
		if entry.Kind != "directory" {
			continue
		}
		if err := removeEmptyMigrationDirectory(project, entry.RelativePath); err != nil {
			return err
		}
	}
	return validateMigrationSources(project, journal, true)
}

func ensureArchiveInventoryDirectory(project *pathguard.Directory, relative string, mode fs.FileMode) error {
	return ensureArchiveInventoryDirectoryWithPrivacy(project, relative, mode, nil, secureArchiveInventoryDirectory, privateMigrationPath)
}

func ensureArchiveInventoryDirectoryWithPrivacy(project *pathguard.Directory, relative string, mode fs.FileMode, beforeSecure func() error, secure func(*os.File) error, private func(string, fs.FileMode) bool) error {
	_, err := atomicfile.EnsureRootDirPrepared(project.Root, filepath.FromSlash(relative), mode.Perm(), beforeSecure, secure)
	if err != nil {
		if errors.Is(err, atomicfile.ErrRootDirectoryIdentityChanged) {
			return staleMigration("legacy archive directory changed before privacy hardening")
		}
		return err
	}
	full := filepath.Join(project.Path, filepath.FromSlash(relative))
	opened, _, err := project.OpenDirectory(relative)
	if err != nil {
		return staleMigration("legacy archive directory collided or was redirected")
	}
	closeErr := opened.Close()
	if closeErr != nil || !private(full, mode) {
		return staleMigration("legacy archive directory mode changed")
	}
	return nil
}

func archiveMigrationFile(project *pathguard.Directory, journal migrationJournal, entry migrationJournalEntry, hooks migrationHooks) error {
	source := entry.RelativePath
	destination := journal.BackupRelative + "/archive/" + strings.TrimPrefix(source, migrationReviewRoot+"/")
	retirement := migrationQuarantinePath(journal, entry, "retired")
	sourceInfo, sourceErr := project.Root.Lstat(filepath.FromSlash(source))
	destinationInfo, destinationErr := project.Root.Lstat(filepath.FromSlash(destination))
	if errors.Is(sourceErr, os.ErrNotExist) && destinationErr == nil {
		if err := verifyMigrationArchiveFile(project, destination, destinationInfo, entry); err != nil {
			return err
		}
		retiredInfo, err := project.Root.Lstat(filepath.FromSlash(retirement))
		if errors.Is(err, os.ErrNotExist) {
			if err := project.Root.Link(filepath.FromSlash(destination), filepath.FromSlash(retirement)); err != nil {
				return staleMigration("legacy retirement quarantine could not be recovered")
			}
			if err := atomicfile.SyncRootPublication(project.Root, filepath.FromSlash(retirement)); err != nil {
				return err
			}
			retiredInfo, err = project.Root.Lstat(filepath.FromSlash(retirement))
		}
		if err != nil || !os.SameFile(destinationInfo, retiredInfo) {
			return staleMigration("legacy retirement quarantine changed")
		}
		return verifyMigrationArchiveFile(project, retirement, retiredInfo, entry)
	}
	if sourceErr == nil && destinationErr == nil {
		if !os.SameFile(sourceInfo, destinationInfo) {
			return staleMigration("legacy archive destination collided with a different identity")
		}
		if err := verifyMigrationArchiveFile(project, source, sourceInfo, entry); err != nil {
			return err
		}
		if err := verifyMigrationArchiveFile(project, destination, destinationInfo, entry); err != nil {
			return err
		}
		if err := atomicfile.SyncRootPublication(project.Root, filepath.FromSlash(destination)); err != nil {
			return err
		}
		if hooks.beforeArchiveRetire != nil {
			if err := hooks.beforeArchiveRetire(source, retirement); err != nil {
				return err
			}
		}
		if err := atomicfile.RenameRootNoReplace(project.Root, filepath.FromSlash(source), filepath.FromSlash(retirement)); err != nil {
			return staleMigration("legacy source could not be retired without replacement")
		}
		if hooks.afterArchiveRetire != nil {
			if err := hooks.afterArchiveRetire(source, retirement); err != nil {
				return err
			}
		}
		retiredInfo, err := project.Root.Lstat(filepath.FromSlash(retirement))
		if err != nil || !os.SameFile(sourceInfo, retiredInfo) {
			return staleMigration("legacy source replacement preserved in retirement quarantine")
		}
		afterMove, err := project.Root.Lstat(filepath.FromSlash(destination))
		if err != nil || !os.SameFile(sourceInfo, afterMove) {
			return staleMigration("legacy archive moved identity changed")
		}
		return verifyMigrationArchiveFile(project, destination, afterMove, entry)
	}
	if sourceErr != nil || (destinationErr != nil && !errors.Is(destinationErr, os.ErrNotExist)) || destinationErr == nil {
		return staleMigration("legacy archive destination collided or source disappeared")
	}
	if err := verifyMigrationSourceFile(project, source, sourceInfo, entry); err != nil {
		return err
	}
	if err := secureArchiveSourceForPublication(filepath.Join(project.Path, filepath.FromSlash(source))); err != nil {
		return err
	}
	if hooks.beforeArchivePublish != nil {
		if err := hooks.beforeArchivePublish(source, destination); err != nil {
			return err
		}
	}
	if err := project.Root.Link(filepath.FromSlash(source), filepath.FromSlash(destination)); err != nil {
		return staleMigration("legacy archive destination collided before publication")
	}
	rollback := func() error {
		published, err := project.Root.Lstat(filepath.FromSlash(destination))
		if err != nil || !os.SameFile(sourceInfo, published) {
			return errors.New("cannot safely roll back legacy archive publication")
		}
		quarantine := migrationQuarantinePath(journal, entry, "rollback")
		if hooks.beforeArchiveRollbackRetire != nil {
			if err := hooks.beforeArchiveRollbackRetire(destination, quarantine); err != nil {
				return err
			}
		}
		if err := atomicfile.RenameRootNoReplace(project.Root, filepath.FromSlash(destination), filepath.FromSlash(quarantine)); err != nil {
			return errors.New("cannot safely quarantine legacy archive rollback")
		}
		moved, err := project.Root.Lstat(filepath.FromSlash(quarantine))
		if err != nil || !os.SameFile(sourceInfo, moved) {
			return staleMigration("archive destination replacement preserved in rollback quarantine")
		}
		return nil
	}
	if hooks.afterArchivePublish != nil {
		if err := hooks.afterArchivePublish(source, destination); err != nil {
			return errors.Join(err, rollback())
		}
	}
	if err := verifyMigrationArchiveFile(project, source, sourceInfo, entry); err != nil {
		return errors.Join(err, rollback())
	}
	currentSource, err := project.Root.Lstat(filepath.FromSlash(source))
	if err != nil || !os.SameFile(sourceInfo, currentSource) {
		return errors.Join(staleMigration("legacy source identity changed after archive publication"), rollback())
	}
	published, err := project.Root.Lstat(filepath.FromSlash(destination))
	if err != nil || !os.SameFile(sourceInfo, published) {
		return errors.Join(staleMigration("legacy archive publication identity changed"), rollback())
	}
	if err := atomicfile.SyncRootPublication(project.Root, filepath.FromSlash(destination)); err != nil {
		return errors.Join(fmt.Errorf("sync legacy archive publication: %w", err), rollback())
	}
	if hooks.beforeArchiveRetire != nil {
		if err := hooks.beforeArchiveRetire(source, retirement); err != nil {
			return errors.Join(err, rollback())
		}
	}
	if err := atomicfile.RenameRootNoReplace(project.Root, filepath.FromSlash(source), filepath.FromSlash(retirement)); err != nil {
		return errors.Join(staleMigration("legacy source could not be retired without replacement"), rollback())
	}
	if hooks.afterArchiveRetire != nil {
		if err := hooks.afterArchiveRetire(source, retirement); err != nil {
			return err
		}
	}
	retiredInfo, err := project.Root.Lstat(filepath.FromSlash(retirement))
	if err != nil || !os.SameFile(sourceInfo, retiredInfo) {
		return staleMigration("legacy source replacement preserved in retirement quarantine")
	}
	afterMove, err := project.Root.Lstat(filepath.FromSlash(destination))
	if err != nil || !os.SameFile(sourceInfo, afterMove) {
		return staleMigration("legacy archive moved identity changed")
	}
	return verifyMigrationArchiveFile(project, destination, afterMove, entry)
}

func migrationQuarantinePath(journal migrationJournal, entry migrationJournalEntry, kind string) string {
	digest := sha256.Sum256([]byte(entry.RelativePath))
	return journal.BackupRelative + "/quarantine/" + kind + "-" + hex.EncodeToString(digest[:])
}

func verifyMigrationArchiveFile(project *pathguard.Directory, relative string, info os.FileInfo, entry migrationJournalEntry) error {
	if info == nil || !info.Mode().IsRegular() || !privateMigrationPath(filepath.Join(project.Path, filepath.FromSlash(relative)), fs.FileMode(entry.Mode)) || info.Size() != entry.Size {
		return staleMigration("legacy archive file identity or mode changed")
	}
	body, found, err := project.ReadRegular(relative, entry.Size)
	if err != nil || !found || int64(len(body)) != entry.Size || hashPrefixed(body) != entry.SHA256 {
		return staleMigration("legacy archive file content changed")
	}
	return nil
}

func verifyMigrationSourceFile(project *pathguard.Directory, relative string, info os.FileInfo, entry migrationJournalEntry) error {
	if info == nil || !info.Mode().IsRegular() || !migrationSourceModeOK(filepath.Join(project.Path, filepath.FromSlash(relative)), fs.FileMode(entry.Mode)) || info.Size() != entry.Size {
		return staleMigration("legacy source identity or mode changed")
	}
	body, found, err := project.ReadRegular(relative, entry.Size)
	if err != nil || !found || int64(len(body)) != entry.Size || hashPrefixed(body) != entry.SHA256 {
		return staleMigration("legacy source content changed")
	}
	return nil
}

func removeEmptyMigrationDirectory(project *pathguard.Directory, relative string) error {
	if _, err := project.Root.Lstat(filepath.FromSlash(relative)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := project.Root.Remove(filepath.FromSlash(relative)); err != nil {
		return staleMigration("legacy directory changed before archive retirement")
	}
	return nil
}

func verifyCommittedMigration(project *pathguard.Directory, journal migrationJournal) error {
	if err := validateMigrationV2(project, journal); err != nil {
		return err
	}
	if err := validateMigrationSources(project, journal, true); err != nil {
		return err
	}
	if active, err := scanActiveMigrationInventory(project); err != nil || len(active) != 0 {
		return errors.New("ordinary migration inventory is not v2-only")
	}
	if err := verifyMigrationBackup(project, journal, true); err != nil {
		return err
	}
	accepted, err := loadV2FromDirectory(project.Path, project, loadHooks{})
	if err != nil || accepted.State.Review.ProjectID != journal.ProjectID {
		return errors.New("migrated v2 state failed accepted-state validation")
	}
	visible := make([]string, 0, 2)
	if err := project.WalkMarkdown(migrationReviewRoot, func(relative string, _ []byte) error {
		visible = append(visible, relative)
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(visible)
	want := []string{HistoryRelativePath, ReviewRelativePath}
	sort.Strings(want)
	if !equalStrings(visible, want) {
		return errors.New("ordinary visible Markdown inventory is not exactly the two v2 documents")
	}
	return nil
}

func migrationEntryExists(directory *pathguard.Directory, relative string) (bool, error) {
	parent, _, err := directory.OpenDirectory(path.Dir(relative))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer parent.Close()
	_, err = parent.Lstat(path.Base(relative))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func ensureMigrationDirectory(directory *pathguard.Directory, relative, privateFrom string) error {
	if directory == nil || directory.Root == nil || !safeMigrationRelative(relative) || !safeMigrationRelative(privateFrom) {
		return errors.New("invalid migration directory")
	}
	components := strings.Split(relative, "/")
	current := ""
	for _, component := range components {
		current = path.Join(current, component)
		privateComponent := current == privateFrom || strings.HasPrefix(current, privateFrom+"/")
		var prepare func(*os.File) error
		if privateComponent {
			prepare = securePrivateMigrationDirectory
		}
		_, err := atomicfile.EnsureRootDirPrepared(directory.Root, filepath.FromSlash(current), 0o700, nil, prepare)
		if err != nil {
			if errors.Is(err, atomicfile.ErrRootDirectoryIdentityChanged) {
				return staleMigration("migration directory changed before privacy hardening")
			}
			return err
		}
		opened, _, err := directory.OpenDirectory(current)
		if err != nil {
			return errors.New("migration directory is redirected or invalid")
		}
		closeErr := opened.Close()
		if closeErr != nil {
			return closeErr
		}
		if privateComponent {
			full := filepath.Join(directory.Path, filepath.FromSlash(current))
			if !privateMigrationPath(full, 0o700) {
				return errors.New("migration private directory permissions are unsafe")
			}
		}
	}
	return nil
}

func writeRootCreateIfAbsent(parent *os.Root, leaf string, body []byte, mode fs.FileMode) error {
	if parent == nil || leaf == "" || filepath.Base(leaf) != leaf {
		return errors.New("invalid create-if-absent target")
	}
	if existing, err := parent.Lstat(leaf); err == nil {
		return verifyRootRegular(parent, leaf, existing, body, mode)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := ".session-reviewer-" + hex.EncodeToString(random)
	file, err := parent.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = parent.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if err := securePrivateMigrationFile(file); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := parent.Link(temporary, leaf); err != nil {
		if existing, inspectErr := parent.Lstat(leaf); inspectErr == nil {
			return verifyRootRegular(parent, leaf, existing, body, mode)
		}
		return err
	}
	if err := atomicfile.SyncRootPublication(parent, leaf); err != nil {
		return err
	}
	if err := atomicfile.RemoveRoot(parent, temporary); err != nil {
		return err
	}
	removeTemporary = false
	created, err := parent.Lstat(leaf)
	if err != nil {
		return err
	}
	return verifyRootRegular(parent, leaf, created, body, mode)
}

func verifyRootRegular(parent *os.Root, leaf string, before os.FileInfo, body []byte, mode fs.FileMode) error {
	if before == nil || !before.Mode().IsRegular() || !privateMigrationPath(filepath.Join(parent.Name(), leaf), mode) || before.Size() != int64(len(body)) {
		return errors.New("create-if-absent destination collision")
	}
	file, err := parent.Open(leaf)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return errors.New("create-if-absent destination changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(len(body))+1))
	closeErr := file.Close()
	after, inspectErr := parent.Lstat(leaf)
	if err != nil || closeErr != nil || inspectErr != nil || !os.SameFile(before, after) || !bytes.Equal(content, body) {
		return errors.New("create-if-absent destination collision")
	}
	return nil
}

func safeMigrationRelative(relative string) bool {
	if relative == "" || strings.Contains(relative, `\`) || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return false
	}
	_, err := platform.PathKey("windows", platform.CaseSensitive, relative)
	return err == nil
}

func migrationPortableInventoryKey(relative string) (string, error) {
	if !safeMigrationRelative(relative) || !strings.HasPrefix(relative, migrationReviewRoot+"/") {
		return "", errors.New("unsafe migration inventory path")
	}
	return platform.PathKey("windows", platform.CaseInsensitive, strings.TrimPrefix(relative, migrationReviewRoot+"/"))
}

func staleMigration(detail string) error {
	if strings.TrimSpace(detail) == "" {
		detail = "migration inputs changed"
	}
	return fmt.Errorf("%w: %s", ErrStaleMigration, detail)
}

func hashPrefixed(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func snapshotJournalFiles(files []ledger.SnapshotFile) []migrationJournalFile {
	result := make([]migrationJournalFile, 0, len(files))
	for _, file := range files {
		result = append(result, migrationJournalFile{RelativePath: file.RelativePath, SHA256: file.SHA256, Size: file.Size, Mode: uint32(file.Perm.Perm())})
	}
	return result
}

func plannedJournalFiles(files []ledger.PlannedFile) []migrationJournalFile {
	result := make([]migrationJournalFile, 0, len(files))
	for _, file := range files {
		result = append(result, migrationJournalFile{RelativePath: file.RelativePath, SHA256: hashPrefixed(file.Data), Size: int64(len(file.Data)), Mode: uint32(file.Perm.Perm())})
	}
	return result
}

func equalSnapshotJournal(files []ledger.SnapshotFile, journal []migrationJournalFile) bool {
	return reflectJournalFiles(snapshotJournalFiles(files), journal)
}

func equalPlannedJournal(files []ledger.PlannedFile, journal []migrationJournalFile) bool {
	return reflectJournalFiles(plannedJournalFiles(files), journal)
}

func reflectJournalFiles(left, right []migrationJournalFile) bool {
	left = append([]migrationJournalFile(nil), left...)
	right = append([]migrationJournalFile(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].RelativePath < left[j].RelativePath })
	sort.Slice(right, func(i, j int) bool { return right[i].RelativePath < right[j].RelativePath })
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func clonePlannedFiles(files []ledger.PlannedFile) []ledger.PlannedFile {
	result := make([]ledger.PlannedFile, len(files))
	for index, file := range files {
		result[index] = file
		result[index].Data = bytes.Clone(file.Data)
		result[index].ExpectedData = bytes.Clone(file.ExpectedData)
	}
	return result
}

func migrationFilePaths(entries []migrationJournalEntry) []string {
	result := make([]string, 0)
	for _, entry := range entries {
		if entry.Kind == "file" {
			result = append(result, entry.RelativePath)
		}
	}
	sort.Strings(result)
	return result
}

func migrationInventoryByPath(entries []migrationJournalEntry) map[string]migrationJournalEntry {
	result := make(map[string]migrationJournalEntry, len(entries))
	for _, entry := range entries {
		result[entry.RelativePath] = entry
	}
	return result
}

func equalMigrationInventory(left, right []migrationJournalEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
