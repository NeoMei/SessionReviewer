package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
	"golang.org/x/text/unicode/norm"
)

const (
	transactionDirectoryName = "transactions"
	maxTransactionBytes      = 64 << 10
)

type TransactionKind string

const (
	TxnEntitySync      TransactionKind = "entity_sync"
	TxnConflictNote    TransactionKind = "conflict_note"
	TxnResolution      TransactionKind = "resolution"
	TxnDerivedPublish  TransactionKind = "derived_publish"
	TxnMachinePublish  TransactionKind = "machine_publish"
	TxnMachineRepair   TransactionKind = "machine_repair"
	TxnConflictRecord  TransactionKind = "conflict_record"
	TxnConflictResolve TransactionKind = "conflict_resolve"
)

type TransactionStage string

const (
	TxnPlanned        TransactionStage = "planned"
	TxnProjectWritten TransactionStage = "project_written"
	TxnVaultWritten   TransactionStage = "vault_written"
	TxnBaseCommitted  TransactionStage = "base_committed"
)

type Transaction struct {
	Version             int              `json:"version"`
	Kind                TransactionKind  `json:"kind"`
	EntityID            string           `json:"entity_id"`
	DesiredHash         string           `json:"desired_hash"`
	ExpectedBaseHash    string           `json:"expected_base_hash"`
	ExpectedProjectHash string           `json:"expected_project_hash,omitempty"`
	ExpectedVaultHash   string           `json:"expected_vault_hash,omitempty"`
	FromPathKey         string           `json:"from_path_key"`
	ToPathKey           string           `json:"to_path_key"`
	Stage               TransactionStage `json:"stage"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

type TransactionStore struct {
	Root *os.Root
}

func (store TransactionStore) Save(next Transaction) (retErr error) {
	if err := validateTransaction(next, ""); err != nil {
		return err
	}
	directory, found, err := store.openTransactionDirectory(next.Stage == TxnPlanned)
	if err != nil || !found {
		if err == nil {
			return errors.New("first transaction stage must be planned")
		}
		return err
	}
	defer func() { retErr = errors.Join(retErr, directory.Close()) }()
	records, _, err := loadAllTransactions(directory, nil)
	if err != nil {
		return err
	}
	var current *Transaction
	for index := range records {
		if records[index].EntityID == next.EntityID {
			copy := records[index]
			current = &copy
			break
		}
	}
	if current != nil {
		if err := validateTransactionTransition(*current, next); err != nil {
			return err
		}
		if reflect.DeepEqual(*current, next) {
			return verifyTransactionDirectoryIdentity(store.Root, directory)
		}
	} else if next.Stage != TxnPlanned {
		return errors.New("first transaction stage must be planned")
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return errors.New("cannot encode transaction journal")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxTransactionBytes {
		return errors.New("transaction journal exceeds size limit")
	}
	name := transactionRecordName(next.Kind, next.EntityID)
	if err := atomicfile.WriteRoot(directory, name, encoded, 0o600); err != nil {
		return transactionFailure{cause: err}
	}
	info, found, err := regularTransactionEntry(directory, name)
	if err != nil || !found {
		return errors.New("transaction journal is missing after save")
	}
	written, err := readTransactionRecord(directory, name, info, nil)
	if err != nil || !reflect.DeepEqual(written, next) {
		return errors.New("transaction journal failed post-save verification")
	}
	return verifyTransactionDirectoryIdentity(store.Root, directory)
}

func (store TransactionStore) Load(entityID string) (Transaction, bool, error) {
	return store.loadWithReadHook(entityID, nil)
}

func (store TransactionStore) loadWithReadHook(entityID string, afterFirstRead func() error) (Transaction, bool, error) {
	if !stableBaseID.MatchString(entityID) {
		return Transaction{}, false, errors.New("invalid transaction entity ID")
	}
	directory, found, err := store.openTransactionDirectory(false)
	if err != nil || !found {
		return Transaction{}, false, err
	}
	defer directory.Close()
	records, _, err := loadAllTransactions(directory, afterFirstRead)
	if err != nil {
		return Transaction{}, false, err
	}
	if err := verifyTransactionDirectoryIdentity(store.Root, directory); err != nil {
		return Transaction{}, false, err
	}
	for _, record := range records {
		if record.EntityID == entityID {
			return record, true, nil
		}
	}
	return Transaction{}, false, nil
}

func (store TransactionStore) List() ([]Transaction, error) {
	directory, found, err := store.openTransactionDirectory(false)
	if err != nil || !found {
		return nil, err
	}
	defer directory.Close()
	records, _, err := loadAllTransactions(directory, nil)
	if err != nil {
		return nil, err
	}
	if err := verifyTransactionDirectoryIdentity(store.Root, directory); err != nil {
		return nil, err
	}
	return append([]Transaction(nil), records...), nil
}

func (store TransactionStore) Remove(entityID string) (retErr error) {
	if !stableBaseID.MatchString(entityID) {
		return errors.New("invalid transaction entity ID")
	}
	directory, found, err := store.openTransactionDirectory(false)
	if err != nil || !found {
		return err
	}
	defer func() { retErr = errors.Join(retErr, directory.Close()) }()
	records, pairs, err := loadAllTransactions(directory, nil)
	if err != nil {
		return err
	}
	var targetName string
	for _, record := range records {
		if record.EntityID == entityID {
			targetName = transactionRecordName(record.Kind, record.EntityID)
			break
		}
	}
	if targetName == "" {
		return verifyTransactionDirectoryIdentity(store.Root, directory)
	}
	pair := pairs[targetName]
	for _, name := range []string{pair.primary, pair.backup} {
		if name == "" {
			continue
		}
		info, present, err := regularTransactionEntry(directory, name)
		if err != nil || !present {
			return errors.New("transaction journal changed before removal")
		}
		now, err := directory.Lstat(name)
		if err != nil || !sameBaseFileMetadata(info, now) {
			return errors.New("transaction journal changed before removal")
		}
		if err := atomicfile.RemoveRoot(directory, name); err != nil {
			return transactionFailure{cause: err}
		}
	}
	remaining, _, err := loadAllTransactions(directory, nil)
	if err != nil {
		return err
	}
	for _, record := range remaining {
		if record.EntityID == entityID {
			return errors.New("transaction journal remains after removal")
		}
	}
	return verifyTransactionDirectoryIdentity(store.Root, directory)
}

func transactionRecordName(kind TransactionKind, entityID string) string {
	digest := sha256.Sum256([]byte(string(kind) + "|" + entityID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func transactionRecordPath(kind TransactionKind, entityID string) string {
	return filepath.ToSlash(filepath.Join(transactionDirectoryName, transactionRecordName(kind, entityID)))
}

func (store TransactionStore) openTransactionDirectory(create bool) (*os.Root, bool, error) {
	if store.Root == nil {
		return nil, false, errors.New("transaction root is required")
	}
	rootBefore, err := store.Root.Stat(".")
	if err != nil || rootBefore == nil || !rootBefore.IsDir() || isStateRedirect(rootBefore) {
		return nil, false, errors.New("transaction root is redirected or invalid")
	}
	before, err := store.Root.Lstat(transactionDirectoryName)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, errors.New("cannot inspect transaction directory")
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.EnsureRootDir(store.Root, transactionDirectoryName, 0o700); err != nil {
			return nil, false, transactionFailure{cause: err}
		}
		before, err = store.Root.Lstat(transactionDirectoryName)
	}
	if err != nil || before == nil || !before.IsDir() || isStateRedirect(before) || !privateStateMode(before, 0o700) {
		return nil, false, errors.New("transaction directory is redirected, invalid, or not private")
	}
	directory, err := store.Root.OpenRoot(transactionDirectoryName)
	if err != nil {
		return nil, false, errors.New("cannot open transaction directory")
	}
	opened, err := directory.Stat(".")
	if err != nil || !os.SameFile(before, opened) || !privateStateMode(opened, 0o700) {
		_ = directory.Close()
		return nil, false, errors.New("transaction directory changed while opening")
	}
	after, inspectErr := store.Root.Lstat(transactionDirectoryName)
	rootAfter, rootErr := store.Root.Stat(".")
	if inspectErr != nil || rootErr != nil || !os.SameFile(before, after) || !os.SameFile(rootBefore, rootAfter) || isStateRedirect(after) || !privateStateMode(after, 0o700) {
		_ = directory.Close()
		return nil, false, errors.New("transaction directory changed while opening")
	}
	return directory, true, nil
}

type transactionStatePair struct {
	primary string
	backup  string
}

func loadAllTransactions(root *os.Root, firstReadHook func() error) ([]Transaction, map[string]transactionStatePair, error) {
	pairs, err := inspectTransactionNames(root)
	if err != nil {
		return nil, nil, err
	}
	records := make([]Transaction, 0, len(pairs))
	seenEntities := make(map[string]struct{}, len(pairs))
	hook := firstReadHook
	for primary, pair := range pairs {
		record, found, err := loadTransactionPair(root, pair, hook)
		hook = nil
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue
		}
		if transactionRecordName(record.Kind, record.EntityID) != primary {
			return nil, nil, errors.New("transaction filename does not certify its kind and entity ID")
		}
		if _, duplicate := seenEntities[record.EntityID]; duplicate {
			return nil, nil, errors.New("duplicate transaction entity ID")
		}
		seenEntities[record.EntityID] = struct{}{}
		records = append(records, record)
	}
	sort.Slice(records, func(first, second int) bool {
		if records[first].EntityID != records[second].EntityID {
			return records[first].EntityID < records[second].EntityID
		}
		return records[first].Kind < records[second].Kind
	})
	return records, pairs, nil
}

func inspectTransactionNames(root *os.Root) (map[string]transactionStatePair, error) {
	entries, err := readBoundedSyncStateEntries(root, maxSyncStateDirectoryEntries, "transaction directory")
	if err != nil {
		return nil, errors.New("cannot inspect transaction directory")
	}
	pairs := make(map[string]transactionStatePair)
	seenFolded := make(map[string]string, len(entries))
	backupSuffix := strings.TrimPrefix(atomicfile.BackupPath("x"), "x")
	for _, entry := range entries {
		name := entry.Name()
		if baseTempName.MatchString(name) {
			if info, found, err := regularTransactionEntry(root, name); err != nil || !found || info == nil {
				return nil, errors.New("transaction temporary file is redirected, invalid, or not private")
			}
			continue
		}
		folded := strings.ToLower(name)
		if previous, found := seenFolded[folded]; found && previous != name {
			return nil, errors.New("transaction journal names collide case-insensitively")
		}
		seenFolded[folded] = name
		primary := name
		backup := false
		if strings.HasSuffix(primary, backupSuffix) {
			primary = strings.TrimSuffix(primary, backupSuffix)
			backup = true
		}
		if !baseJSONName.MatchString(primary) || primary != strings.ToLower(primary) {
			return nil, errors.New("invalid transaction journal filename")
		}
		pair := pairs[primary]
		pair.primary = primary
		if backup {
			pair.backup = name
		}
		pairs[primary] = pair
	}
	return pairs, nil
}

func loadTransactionPair(root *os.Root, pair transactionStatePair, firstReadHook func() error) (Transaction, bool, error) {
	primaryInfo, primaryFound, err := regularTransactionEntry(root, pair.primary)
	if err != nil {
		return Transaction{}, false, err
	}
	backupInfo, backupFound, err := regularTransactionEntry(root, pair.backup)
	if err != nil {
		return Transaction{}, false, err
	}
	if !primaryFound && !backupFound {
		return Transaction{}, false, nil
	}
	var primary, backup Transaction
	if primaryFound {
		primary, err = readTransactionRecord(root, pair.primary, primaryInfo, firstReadHook)
		firstReadHook = nil
		if err != nil {
			return Transaction{}, false, err
		}
	}
	if backupFound {
		backup, err = readTransactionRecord(root, pair.backup, backupInfo, firstReadHook)
		if err != nil {
			return Transaction{}, false, err
		}
	}
	if primaryFound && transactionRecordName(primary.Kind, primary.EntityID) != pair.primary {
		return Transaction{}, false, errors.New("transaction filename does not certify its kind and entity ID")
	}
	if backupFound && transactionRecordName(backup.Kind, backup.EntityID) != pair.primary {
		return Transaction{}, false, errors.New("transaction recovery filename does not certify its kind and entity ID")
	}
	if primaryFound && backupFound && (primary.Kind != backup.Kind || primary.EntityID != backup.EntityID ||
		primary.Version != backup.Version || primary.DesiredHash != backup.DesiredHash ||
		primary.ExpectedBaseHash != backup.ExpectedBaseHash || primary.FromPathKey != backup.FromPathKey ||
		primary.ToPathKey != backup.ToPathKey) {
		return Transaction{}, false, errors.New("transaction recovery backup does not match the active journal")
	}
	if primaryFound {
		return primary, true, nil
	}
	return backup, true, nil
}

func regularTransactionEntry(root *os.Root, name string) (os.FileInfo, bool, error) {
	if name == "" {
		return nil, false, nil
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("cannot inspect transaction journal")
	}
	if isStateRedirect(info) || !info.Mode().IsRegular() || !privateStateMode(info, 0o600) {
		return nil, true, errors.New("transaction journal is redirected, invalid, or not private")
	}
	return info, true, nil
}

func readTransactionRecord(root *os.Root, name string, before os.FileInfo, afterFirstRead func() error) (Transaction, error) {
	if before.Size() > maxTransactionBytes {
		return Transaction{}, errors.New("transaction journal exceeds size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return Transaction{}, errors.New("cannot open transaction journal")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(before, opened) || isStateRedirect(opened) || !privateStateMode(opened, 0o600) {
		return Transaction{}, errors.New("transaction journal changed while opening")
	}
	encoded, err := readTransactionSnapshot(file)
	if err != nil {
		return Transaction{}, err
	}
	if afterFirstRead != nil {
		if err := afterFirstRead(); err != nil {
			return Transaction{}, errors.New("transaction journal changed while reading")
		}
	}
	middle, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(opened, middle) {
		return Transaction{}, errors.New("transaction journal changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Transaction{}, errors.New("transaction journal cannot be reread")
	}
	second, err := readTransactionSnapshot(file)
	if err != nil {
		return Transaction{}, err
	}
	afterOpen, statErr := file.Stat()
	afterName, inspectErr := root.Lstat(name)
	if statErr != nil || inspectErr != nil || !sameBaseFileMetadata(opened, afterOpen) || !sameBaseFileMetadata(opened, afterName) || isStateRedirect(afterName) || !privateStateMode(afterName, 0o600) || !bytes.Equal(encoded, second) {
		return Transaction{}, errors.New("transaction journal changed while reading")
	}
	if err := validateFlatTransactionJSON(encoded); err != nil {
		return Transaction{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record Transaction
	if err := decoder.Decode(&record); err != nil {
		return Transaction{}, errors.New("transaction journal is corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Transaction{}, errors.New("transaction journal is corrupt")
	}
	if err := validateTransaction(record, ""); err != nil {
		return Transaction{}, err
	}
	return record, nil
}

func readTransactionSnapshot(file *os.File) ([]byte, error) {
	encoded, err := io.ReadAll(io.LimitReader(file, maxTransactionBytes+1))
	if err != nil || len(encoded) > maxTransactionBytes || !utf8.Valid(encoded) {
		return nil, errors.New("transaction journal is corrupt")
	}
	return encoded, nil
}

func validateFlatTransactionJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("transaction journal is corrupt")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("transaction journal is corrupt")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("transaction journal contains duplicate fields")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("transaction journal is corrupt")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("transaction journal is corrupt")
	}
	return nil
}

func validateTransaction(record Transaction, expectedEntityID string) error {
	if record.Version != 1 {
		return errors.New("invalid transaction version")
	}
	switch record.Kind {
	case TxnEntitySync, TxnConflictNote, TxnResolution, TxnDerivedPublish, TxnMachinePublish, TxnMachineRepair, TxnConflictRecord, TxnConflictResolve:
	default:
		return errors.New("invalid transaction kind")
	}
	if !stableBaseID.MatchString(record.EntityID) || (expectedEntityID != "" && record.EntityID != expectedEntityID) {
		return errors.New("invalid transaction entity ID")
	}
	if !lowerSHA256.MatchString(record.DesiredHash) ||
		(record.ExpectedBaseHash == "" && record.Kind != TxnEntitySync) ||
		(record.ExpectedBaseHash != "" && !lowerSHA256.MatchString(record.ExpectedBaseHash)) {
		return errors.New("invalid transaction hash")
	}
	for _, expected := range []string{record.ExpectedProjectHash, record.ExpectedVaultHash} {
		if expected != "" && !lowerSHA256.MatchString(expected) {
			return errors.New("invalid transaction target hash")
		}
	}
	if record.Kind != TxnEntitySync && record.Kind != TxnMachinePublish && record.Kind != TxnMachineRepair && record.Kind != TxnConflictRecord && record.Kind != TxnConflictResolve &&
		(record.ExpectedProjectHash != "" || record.ExpectedVaultHash != "") {
		return errors.New("invalid transaction target hash owner")
	}
	if record.Kind == TxnDerivedPublish && (record.EntityID != derivedTransactionID || record.ExpectedBaseHash == "" || record.FromPathKey != "" || record.ToPathKey != "") {
		return errors.New("invalid derived transaction")
	}
	if record.Kind == TxnMachinePublish && (record.EntityID != machineLedgerEntityID || record.ExpectedBaseHash == "" || record.ExpectedProjectHash != record.ExpectedBaseHash || record.FromPathKey != "" || record.ToPathKey != "") {
		return errors.New("invalid machine publication transaction")
	}
	if record.Kind == TxnMachineRepair && (record.EntityID != machineLedgerRepairEntityID || record.ExpectedBaseHash == "" || record.ExpectedProjectHash != record.ExpectedBaseHash || record.ExpectedVaultHash == "" || record.FromPathKey != "" || record.ToPathKey != "") {
		return errors.New("invalid machine repair transaction")
	}
	if record.Kind == TxnConflictRecord && (!strings.HasPrefix(record.EntityID, "conflict-") || record.ExpectedBaseHash != record.DesiredHash || record.FromPathKey != "" || record.ToPathKey != "") {
		return errors.New("invalid hidden conflict transaction")
	}
	if record.Kind == TxnConflictResolve && (!strings.HasPrefix(record.EntityID, "conflict-") || record.ExpectedProjectHash != record.ExpectedBaseHash || record.ExpectedVaultHash != record.ExpectedBaseHash || record.FromPathKey != "" || record.ToPathKey != "") {
		return errors.New("invalid hidden conflict resolution transaction")
	}
	switch record.Stage {
	case TxnPlanned, TxnProjectWritten, TxnVaultWritten, TxnBaseCommitted:
	default:
		return errors.New("invalid transaction stage")
	}
	if record.UpdatedAt.IsZero() || record.UpdatedAt.Location() != time.UTC {
		return errors.New("transaction update time must be UTC")
	}
	if (record.FromPathKey == "") != (record.ToPathKey == "") {
		return errors.New("transaction rename path keys must be paired")
	}
	if record.FromPathKey != "" {
		if record.FromPathKey == record.ToPathKey || !normalizedTransactionPathKey(record.FromPathKey) || !normalizedTransactionPathKey(record.ToPathKey) {
			return errors.New("invalid transaction rename path key")
		}
	}
	return nil
}

func normalizedTransactionPathKey(value string) bool {
	if value != norm.NFC.String(value) || strings.Contains(value, `\`) {
		return false
	}
	canonical, err := platform.PathKey("darwin", platform.CaseSensitive, value)
	return err == nil && canonical == value
}

func validateTransactionTransition(current, next Transaction) error {
	if current.Kind != next.Kind || current.EntityID != next.EntityID || current.Version != next.Version ||
		current.DesiredHash != next.DesiredHash || current.ExpectedBaseHash != next.ExpectedBaseHash ||
		current.ExpectedProjectHash != next.ExpectedProjectHash || current.ExpectedVaultHash != next.ExpectedVaultHash ||
		current.FromPathKey != next.FromPathKey || current.ToPathKey != next.ToPathKey {
		return errors.New("transaction immutable fields changed")
	}
	if next.UpdatedAt.Before(current.UpdatedAt) {
		return errors.New("transaction update time moved backwards")
	}
	currentStage, currentOK := transactionStageIndex(current.Stage)
	nextStage, nextOK := transactionStageIndex(next.Stage)
	if !currentOK || !nextOK || nextStage < currentStage || nextStage > currentStage+1 {
		return errors.New("invalid transaction stage transition")
	}
	return nil
}

func transactionStageIndex(stage TransactionStage) (int, bool) {
	switch stage {
	case TxnPlanned:
		return 0, true
	case TxnProjectWritten:
		return 1, true
	case TxnVaultWritten:
		return 2, true
	case TxnBaseCommitted:
		return 3, true
	default:
		return 0, false
	}
}

func verifyTransactionDirectoryIdentity(parent, directory *os.Root) error {
	if parent == nil || directory == nil {
		return errors.New("transaction root and directory are required")
	}
	pinned, err := directory.Stat(".")
	if err != nil || pinned == nil || !pinned.IsDir() || isStateRedirect(pinned) || !privateStateMode(pinned, 0o700) {
		return errors.New("pinned transaction directory is invalid")
	}
	namespace, err := parent.Lstat(transactionDirectoryName)
	if err != nil || namespace == nil || !namespace.IsDir() || isStateRedirect(namespace) || !os.SameFile(pinned, namespace) || !privateStateMode(namespace, 0o700) {
		return errors.New("transaction directory namespace identity changed")
	}
	reopened, err := parent.OpenRoot(transactionDirectoryName)
	if err != nil {
		return errors.New("cannot re-open transaction directory")
	}
	defer reopened.Close()
	opened, err := reopened.Stat(".")
	if err != nil || !os.SameFile(namespace, opened) || !os.SameFile(pinned, opened) || isStateRedirect(opened) || !privateStateMode(opened, 0o700) {
		return errors.New("transaction directory changed while re-opening")
	}
	return nil
}

type transactionFailure struct{ cause error }

func (transactionFailure) Error() string { return "transaction state operation failed" }
func (failure transactionFailure) Unwrap() error {
	return failure.cause
}
