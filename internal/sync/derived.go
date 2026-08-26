package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

const derivedTransactionID = "derived-navigation"

const (
	machineLedgerEntityID       = "machine-ledger"
	machineLedgerRepairEntityID = "machine-ledger-repair"
)

type machineSnapshot struct {
	projectBody []byte
	projectHash string
	ledger      reviewv2.MachineLedger
	vaultBody   []byte
	vaultHash   string
	vaultFound  bool
	humanStale  bool
	operations  []Operation
}

func (snapshot machineSnapshot) needsPublish() bool {
	return !snapshot.vaultFound || snapshot.humanStale
}

func (snapshot machineSnapshot) report() MachineReport {
	state := MachineCurrent
	if len(snapshot.operations) != 0 || snapshot.humanStale {
		state = MachinePending
	}
	return MachineReport{State: state, Operations: append([]Operation(nil), snapshot.operations...)}
}

func (engine *Engine) machineVaultRelativePath() string {
	return path.Join(engine.options.VaultReviewPath, ".session-reviewer/ledger.json")
}

func (engine *Engine) planMachineLedger() (machineSnapshot, MachineReport, error) {
	return engine.loadMachineLedgerSnapshot(false)
}

func (engine *Engine) loadMachineLedgerSnapshot(allowModifiedVault bool) (machineSnapshot, MachineReport, error) {
	body, found, err := engine.project.ReadRegular(reviewv2.MachineLedgerRelativePath, int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil || !found {
		return machineSnapshot{}, MachineReport{State: MachineBlocked, Operations: []Operation{}}, errors.New("project machine ledger is unavailable")
	}
	ledgerValue, err := reviewv2.ParseMachineLedger(body)
	if err != nil || ledgerValue.ProjectID != engine.options.ProjectID {
		return machineSnapshot{}, MachineReport{State: MachineBlocked, Operations: []Operation{}}, errors.New("project machine ledger is invalid")
	}
	canonical, err := reviewv2.RenderMachineLedger(ledgerValue)
	if err != nil || !bytes.Equal(body, canonical) {
		return machineSnapshot{}, MachineReport{State: MachineBlocked, Operations: []Operation{}}, errors.New("project machine ledger is not canonical")
	}
	snapshot := machineSnapshot{projectBody: bytes.Clone(body), projectHash: syncdoc.ContentHash(body), ledger: ledgerValue, operations: []Operation{}}
	snapshot.humanStale = engine.machineLedgerBehindProject(ledgerValue)
	vaultBody, vaultFound, err := engine.vault.ReadRegularOptional(engine.machineVaultRelativePath(), int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil {
		return machineSnapshot{}, MachineReport{State: MachineBlocked, Operations: []Operation{}}, errors.New("vault machine ledger is unavailable")
	}
	snapshot.vaultBody = bytes.Clone(vaultBody)
	snapshot.vaultFound = vaultFound
	if vaultFound {
		snapshot.vaultHash = syncdoc.ContentHash(vaultBody)
		if !bytes.Equal(body, vaultBody) {
			if !allowModifiedVault {
				return snapshot, MachineReport{State: MachineBlocked, Operations: []Operation{}}, errors.New("vault machine ledger was modified")
			}
			snapshot.operations = machineLedgerOperations(snapshot, snapshot.projectHash)
			return snapshot, snapshot.report(), nil
		}
		return snapshot, snapshot.report(), nil
	}
	snapshot.operations = []Operation{{
		EntityID: machineLedgerEntityID, Kind: OperationAddVault, Target: SideVault,
		RelativePath: ".session-reviewer/ledger.json",
	}}
	return snapshot, snapshot.report(), nil
}

func (engine *Engine) machineLedgerBehindProject(machine reviewv2.MachineLedger) bool {
	reviewBody, reviewFound, reviewErr := engine.project.ReadRegular(reviewv2.ReviewRelativePath, int64(reviewv2.MaxDocumentBytes))
	historyBody, historyFound, historyErr := engine.project.ReadRegular(reviewv2.HistoryRelativePath, int64(reviewv2.MaxDocumentBytes))
	if reviewErr != nil || historyErr != nil || !reviewFound || !historyFound {
		return false
	}
	review, reviewErr := reviewv2.ParseReview(reviewBody)
	history, historyErr := reviewv2.ParseHistory(historyBody)
	if reviewErr != nil || historyErr != nil || review.Model.ProjectID != machine.ProjectID || history.ProjectID != machine.ProjectID || review.Model.Revision != history.Revision {
		return false
	}
	return machine.AcceptedRevision != review.Model.Revision || machine.ReviewSHA256 != syncdoc.ContentHash(reviewBody) || machine.HistorySHA256 != syncdoc.ContentHash(historyBody)
}

func (engine *Engine) renderNextMachineLedger(snapshot machineSnapshot, committed bool) ([]byte, error) {
	current, found, err := engine.project.ReadRegular(reviewv2.MachineLedgerRelativePath, int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil || !found || syncdoc.ContentHash(current) != snapshot.projectHash || !bytes.Equal(current, snapshot.projectBody) {
		return nil, errors.New("project machine ledger changed after planning")
	}
	if !committed {
		return bytes.Clone(snapshot.projectBody), nil
	}
	reviewBody, found, err := engine.project.ReadRegular(reviewv2.ReviewRelativePath, int64(reviewv2.MaxDocumentBytes))
	if err != nil || !found {
		return nil, errors.New("project review is unavailable")
	}
	historyBody, found, err := engine.project.ReadRegular(reviewv2.HistoryRelativePath, int64(reviewv2.MaxDocumentBytes))
	if err != nil || !found {
		return nil, errors.New("project history is unavailable")
	}
	return engine.renderMachineLedgerForAccepted(snapshot, reviewBody, historyBody, true)
}

func (engine *Engine) renderMachineLedgerForAccepted(snapshot machineSnapshot, reviewBody, historyBody []byte, committed bool) ([]byte, error) {
	if !committed {
		return bytes.Clone(snapshot.projectBody), nil
	}
	reviewDocument, err := reviewv2.ParseReview(reviewBody)
	if err != nil {
		return nil, errors.New("project review is invalid")
	}
	historyDocument, err := reviewv2.ParseHistory(historyBody)
	if err != nil || historyDocument.ProjectID != reviewDocument.Model.ProjectID || historyDocument.Revision != reviewDocument.Model.Revision {
		return nil, errors.New("project human documents do not share one accepted revision")
	}
	next := snapshot.ledger
	next.AcceptedRevision = reviewDocument.Model.Revision
	next.ReviewSHA256 = syncdoc.ContentHash(reviewBody)
	next.HistorySHA256 = syncdoc.ContentHash(historyBody)
	next.LastSuccessfulSync = engine.options.Now().UTC().Format(time.RFC3339Nano)
	return reviewv2.RenderMachineLedger(next)
}

func (engine *Engine) publishMachineLedger(ctx context.Context, snapshot machineSnapshot, committed, repair bool) (MachineReport, error) {
	desired, err := engine.renderNextMachineLedger(snapshot, committed)
	if err != nil {
		return MachineReport{State: MachineBlocked, Operations: []Operation{}}, err
	}
	desiredHash := syncdoc.ContentHash(desired)
	operations := machineLedgerOperations(snapshot, desiredHash)
	if len(operations) == 0 {
		return MachineReport{State: MachineCurrent, Operations: []Operation{}}, nil
	}
	kind := TxnMachinePublish
	entityID := machineLedgerEntityID
	if repair {
		kind = TxnMachineRepair
		entityID = machineLedgerRepairEntityID
	}
	txn := Transaction{
		Version: 1, Kind: kind, EntityID: entityID, DesiredHash: desiredHash,
		ExpectedBaseHash: snapshot.projectHash, ExpectedProjectHash: snapshot.projectHash,
		ExpectedVaultHash: snapshot.vaultHash, Stage: TxnPlanned, UpdatedAt: engine.options.Now().UTC(),
	}
	if err := engine.transactions.Save(txn); err != nil {
		return MachineReport{State: MachineBlocked, Operations: operations}, err
	}
	if err := engine.resumeMachineLedger(ctx, snapshot, desired, txn); err != nil {
		return MachineReport{State: MachineBlocked, Operations: operations}, err
	}
	return MachineReport{State: MachineCurrent, Operations: operations}, nil
}

func machineLedgerOperations(snapshot machineSnapshot, desiredHash string) []Operation {
	operations := []Operation{}
	if snapshot.projectHash != desiredHash {
		operations = append(operations, Operation{
			EntityID: machineLedgerEntityID, Kind: OperationUpdateProject, Target: SideProject,
			RelativePath: ".session-reviewer/ledger.json", BeforeHash: snapshot.projectHash,
		})
	}
	if !snapshot.vaultFound || snapshot.vaultHash != desiredHash {
		kind := OperationUpdateVault
		if !snapshot.vaultFound {
			kind = OperationAddVault
		}
		operations = append(operations, Operation{
			EntityID: machineLedgerEntityID, Kind: kind, Target: SideVault,
			RelativePath: ".session-reviewer/ledger.json", BeforeHash: snapshot.vaultHash,
		})
	}
	return operations
}

func (engine *Engine) resumeMachineLedger(ctx context.Context, snapshot machineSnapshot, desired []byte, txn Transaction) error {
	if txn.Stage == TxnPlanned {
		if err := engine.writeMachineSide(ctx, SideProject, reviewv2.MachineLedgerRelativePath, desired, snapshot.projectBody, true); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnProjectWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnProjectWritten {
		if err := engine.writeMachineSide(ctx, SideVault, engine.machineVaultRelativePath(), desired, snapshot.vaultBody, snapshot.vaultFound); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnVaultWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnVaultWritten {
		if err := engine.verifyMachineLedger(desired); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnBaseCommitted); err != nil {
			return err
		}
	}
	if txn.Stage == TxnBaseCommitted {
		if err := engine.verifyMachineLedger(desired); err != nil {
			return err
		}
	}
	return engine.transactions.Remove(txn.EntityID)
}

func (engine *Engine) verifyMachineLedger(desired []byte) error {
	for _, target := range []struct {
		directory *pathguard.Directory
		relative  string
	}{{engine.project, reviewv2.MachineLedgerRelativePath}, {engine.vault, engine.machineVaultRelativePath()}} {
		body, found, err := target.directory.ReadRegular(target.relative, int64(reviewv2.MaxMachineLedgerBytes))
		if err != nil || !found || !bytes.Equal(body, desired) {
			return errors.New("machine ledger publication did not converge")
		}
	}
	return nil
}

func (engine *Engine) recoverMachineLedger(ctx context.Context, txn Transaction) error {
	projectBody, found, err := engine.project.ReadRegular(reviewv2.MachineLedgerRelativePath, int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil || !found {
		return errors.New("interrupted project machine ledger is unavailable")
	}
	projectHash := syncdoc.ContentHash(projectBody)
	if projectHash != txn.DesiredHash {
		if txn.Stage == TxnPlanned && projectHash == txn.ExpectedProjectHash {
			return engine.transactions.Remove(txn.EntityID)
		}
		return errors.New("interrupted project machine ledger changed")
	}
	vaultBody, vaultFound, err := engine.vault.ReadRegularOptional(engine.machineVaultRelativePath(), int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil {
		return err
	}
	vaultHash := ""
	if vaultFound {
		vaultHash = syncdoc.ContentHash(vaultBody)
		if vaultHash != txn.ExpectedVaultHash && vaultHash != txn.DesiredHash {
			return errors.New("interrupted vault machine ledger changed")
		}
	} else if txn.ExpectedVaultHash != "" {
		return errors.New("interrupted vault machine ledger disappeared")
	}
	snapshot := machineSnapshot{
		projectBody: bytes.Clone(projectBody), projectHash: txn.ExpectedProjectHash,
		vaultBody: bytes.Clone(vaultBody), vaultHash: txn.ExpectedVaultHash, vaultFound: txn.ExpectedVaultHash != "",
	}
	return engine.resumeMachineLedger(ctx, snapshot, projectBody, txn)
}

func (engine *Engine) writeMachineSide(ctx context.Context, side Side, relative string, desired, expected []byte, expectedFound bool) error {
	directory := engine.project
	if side == SideVault {
		directory = engine.vault
	}
	current, found, err := directory.ReadRegularOptional(relative, int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil {
		return err
	}
	if found && bytes.Equal(current, desired) {
		return nil
	}
	if found != expectedFound || (found && !bytes.Equal(current, expected)) {
		return errors.New("machine ledger changed after planning")
	}
	if parent := path.Dir(relative); parent != "." {
		if err := directory.EnsureDirectory(parent, 0o700); err != nil {
			return err
		}
	}
	if err := engine.writer.WriteIfUnchanged(ctx, side, relative, desired, 0o600, expected, expectedFound); err != nil {
		return err
	}
	written, found, err := directory.ReadRegular(relative, int64(reviewv2.MaxMachineLedgerBytes))
	if err != nil || !found || !bytes.Equal(written, desired) {
		return errors.New("machine ledger target verification failed")
	}
	return nil
}

type DerivedPublishState string

const (
	DerivedCurrent  DerivedPublishState = "current"
	DerivedPending  DerivedPublishState = "pending"
	DerivedDeferred DerivedPublishState = "deferred"
	DerivedFailed   DerivedPublishState = "failed"
)

type DerivedReport struct {
	State      DerivedPublishState `json:"state"`
	Files      int                 `json:"files"`
	Operations []Operation         `json:"operations"`
}

type derivedPlan struct {
	artifacts    []ledger.DerivedArtifact
	operations   []Operation
	baseChanges  map[string]BaseRecord
	manifestHash string
	semanticHash string
}

func (plan derivedPlan) changed() bool {
	return len(plan.operations) != 0 || len(plan.baseChanges) != 0
}

func (plan derivedPlan) report() DerivedReport {
	state := DerivedCurrent
	if plan.changed() {
		state = DerivedPending
	}
	return DerivedReport{State: state, Files: len(plan.artifacts), Operations: append([]Operation(nil), plan.operations...)}
}

func (engine *Engine) planDerived() (derivedPlan, error) {
	state, err := ledger.Load(engine.options.ProjectRoot)
	if err != nil {
		return derivedPlan{}, errors.New("accepted ledger cannot be loaded for derived publication")
	}
	artifacts, err := ledger.RenderDerivedArtifacts(state)
	if err != nil {
		return derivedPlan{}, errors.New("derived navigation cannot be rendered")
	}
	plan := derivedPlan{artifacts: artifacts, baseChanges: make(map[string]BaseRecord)}
	projectKeys := make(map[string]string, len(artifacts))
	vaultKeys := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		if len(artifact.Data) > syncdoc.MaxDocumentBytes || !strings.HasPrefix(artifact.RelativePath, "docs/session-review/") {
			return derivedPlan{}, errors.New("derived navigation artifact is invalid")
		}
		within := strings.TrimPrefix(artifact.RelativePath, "docs/session-review/")
		if within == "" || path.Clean(within) != within {
			return derivedPlan{}, errors.New("derived navigation artifact path is invalid")
		}
		projectKey, err := platform.PathKey(engine.options.GOOS, platform.CaseSensitive, artifact.RelativePath)
		if err != nil {
			return derivedPlan{}, errors.New("derived project path is invalid")
		}
		vaultRelative := path.Join(engine.options.VaultReviewPath, within)
		vaultKey, err := platform.PathKey(engine.options.GOOS, engine.options.VaultCaseMode, vaultRelative)
		if err != nil {
			return derivedPlan{}, errors.New("derived vault path is invalid")
		}
		if previous, exists := projectKeys[projectKey]; exists && previous != artifact.RelativePath {
			return derivedPlan{}, errors.New("derived project paths collide")
		}
		if previous, exists := vaultKeys[vaultKey]; exists && previous != vaultRelative {
			return derivedPlan{}, errors.New("derived vault paths collide")
		}
		projectKeys[projectKey], vaultKeys[vaultKey] = artifact.RelativePath, vaultRelative
		var canonical syncdoc.Document
		var base BaseRecord
		baseFound := false
		if artifact.EntityID != "" {
			canonical, err = syncdoc.Parse(within, artifact.Data)
			if err != nil {
				return derivedPlan{}, errors.New("derived entity document is invalid")
			}
			base, baseFound, err = engine.bases.Load(artifact.EntityID)
			if err != nil {
				return derivedPlan{}, errors.New("derived entity merge base is unavailable")
			}
			if baseFound {
				baseDocument, err := syncdoc.Parse(base.RelativePath, base.Content)
				if err != nil || base.RelativePath != within || !canonical.SemanticEqual(baseDocument) {
					return derivedPlan{}, errors.New("derived entity differs semantically from merge base")
				}
			}
		}
		for _, target := range []struct {
			side      Side
			relative  string
			directory interface {
				ReadRegularOptional(string, int64) ([]byte, bool, error)
			}
		}{{SideProject, artifact.RelativePath, engine.project}, {SideVault, vaultRelative, engine.vault}} {
			current, found, err := target.directory.ReadRegularOptional(target.relative, int64(syncdoc.MaxDocumentBytes))
			if err != nil {
				return derivedPlan{}, errors.New("derived target cannot be read safely")
			}
			if found && baseFound {
				currentDocument, err := syncdoc.Parse(within, current)
				if err != nil || !canonical.SemanticEqual(currentDocument) {
					return derivedPlan{}, errors.New("derived target changed semantically after reconciliation")
				}
			}
			if found && bytes.Equal(current, artifact.Data) {
				continue
			}
			kind := OperationUpdateProject
			if target.side == SideVault {
				kind = OperationUpdateVault
			}
			operation := Operation{EntityID: derivedTransactionID, Kind: kind, Target: target.side, RelativePath: within, AfterHash: syncdoc.ContentHash(artifact.Data)}
			if found {
				operation.BeforeHash = syncdoc.ContentHash(current)
			} else if target.side == SideProject {
				operation.Kind = OperationAddProject
			} else {
				operation.Kind = OperationAddVault
			}
			plan.operations = append(plan.operations, operation)
		}
		if artifact.EntityID == "" {
			continue
		}
		if !baseFound || !bytes.Equal(base.Content, artifact.Data) {
			plan.baseChanges[artifact.EntityID] = base
		}
	}
	sort.Slice(plan.operations, func(i, j int) bool {
		if plan.operations[i].RelativePath != plan.operations[j].RelativePath {
			return plan.operations[i].RelativePath < plan.operations[j].RelativePath
		}
		if plan.operations[i].Target != plan.operations[j].Target {
			return plan.operations[i].Target < plan.operations[j].Target
		}
		return plan.operations[i].Kind < plan.operations[j].Kind
	})
	plan.manifestHash = hashDerivedManifest(artifacts, false)
	plan.semanticHash = hashDerivedManifest(artifacts, true)
	return plan, nil
}

func (engine *Engine) publishDerived(ctx context.Context, plan derivedPlan) error {
	txn := Transaction{Version: 1, Kind: TxnDerivedPublish, EntityID: derivedTransactionID, DesiredHash: plan.manifestHash, ExpectedBaseHash: plan.semanticHash, Stage: TxnPlanned, UpdatedAt: engine.options.Now().UTC()}
	if err := engine.transactions.Save(txn); err != nil {
		return err
	}
	return engine.resumeDerived(ctx, plan, txn)
}

func (engine *Engine) recoverDerived(ctx context.Context, txn Transaction) error {
	plan, err := engine.planDerived()
	if err != nil {
		return err
	}
	if plan.manifestHash != txn.DesiredHash || plan.semanticHash != txn.ExpectedBaseHash {
		return errors.New("derived recovery manifest does not match accepted ledger")
	}
	return engine.resumeDerived(ctx, plan, txn)
}

func (engine *Engine) resumeDerived(ctx context.Context, plan derivedPlan, txn Transaction) error {
	if txn.Stage == TxnPlanned {
		if err := engine.writeDerivedSide(ctx, SideProject, plan); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnProjectWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnProjectWritten {
		if err := engine.writeDerivedSide(ctx, SideVault, plan); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnVaultWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnVaultWritten {
		if err := engine.commitDerivedBases(plan); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnBaseCommitted); err != nil {
			return err
		}
	}
	if err := engine.verifyDerived(plan); err != nil {
		return err
	}
	return engine.transactions.Remove(derivedTransactionID)
}

func (engine *Engine) advanceDerived(txn *Transaction, stage TransactionStage) error {
	txn.Stage = stage
	txn.UpdatedAt = engine.options.Now().UTC()
	return engine.transactions.Save(*txn)
}

func (engine *Engine) writeDerivedSide(ctx context.Context, side Side, plan derivedPlan) error {
	directory := engine.project
	if side == SideVault {
		directory = engine.vault
	}
	for _, artifact := range plan.artifacts {
		relative := artifact.RelativePath
		if side == SideVault {
			relative = path.Join(engine.options.VaultReviewPath, strings.TrimPrefix(artifact.RelativePath, "docs/session-review/"))
		}
		current, found, err := directory.ReadRegularOptional(relative, int64(syncdoc.MaxDocumentBytes))
		if err != nil {
			return err
		}
		if found && bytes.Equal(current, artifact.Data) {
			continue
		}
		if parent := path.Dir(relative); parent != "." {
			if err := directory.EnsureDirectory(parent, 0o700); err != nil {
				return err
			}
		}
		if err := engine.writer.WriteIfUnchanged(ctx, side, relative, artifact.Data, 0o644, current, found); err != nil {
			return err
		}
		written, found, err := directory.ReadRegular(relative, int64(syncdoc.MaxDocumentBytes))
		if err != nil || !found || !bytes.Equal(written, artifact.Data) {
			return errors.New("derived target verification failed")
		}
	}
	return nil
}

func (engine *Engine) commitDerivedBases(plan derivedPlan) error {
	for _, artifact := range plan.artifacts {
		if artifact.EntityID == "" {
			continue
		}
		relative := strings.TrimPrefix(artifact.RelativePath, "docs/session-review/")
		current, found, err := engine.bases.Load(artifact.EntityID)
		if err != nil {
			return errors.New("derived merge base is unavailable")
		}
		if found && bytes.Equal(current.Content, artifact.Data) {
			continue
		}
		canonical, canonicalErr := syncdoc.Parse(relative, artifact.Data)
		if canonicalErr != nil {
			return errors.New("derived merge base changed semantically")
		}
		if found {
			baseDocument, baseErr := syncdoc.Parse(current.RelativePath, current.Content)
			if baseErr != nil || current.RelativePath != relative || !canonical.SemanticEqual(baseDocument) {
				return errors.New("derived merge base changed semantically")
			}
		}
		hash := syncdoc.ContentHash(artifact.Data)
		next := BaseRecord{Version: 1, EntityID: artifact.EntityID, RelativePath: relative, ContentHash: hash, ProjectHash: hash, VaultHash: hash, Content: bytes.Clone(artifact.Data), SyncedAt: engine.options.Now().UTC()}
		expected := ""
		if found {
			expected = current.ContentHash
		}
		if err := engine.bases.Commit(expected, next); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) verifyDerived(plan derivedPlan) error {
	for _, artifact := range plan.artifacts {
		for _, target := range []struct {
			directory interface {
				ReadRegular(string, int64) ([]byte, bool, error)
			}
			relative string
		}{{engine.project, artifact.RelativePath}, {engine.vault, path.Join(engine.options.VaultReviewPath, strings.TrimPrefix(artifact.RelativePath, "docs/session-review/"))}} {
			body, found, err := target.directory.ReadRegular(target.relative, int64(syncdoc.MaxDocumentBytes))
			if err != nil || !found || !bytes.Equal(body, artifact.Data) {
				return errors.New("derived publication did not converge")
			}
		}
		if artifact.EntityID != "" {
			base, found, err := engine.bases.Load(artifact.EntityID)
			if err != nil || !found || !bytes.Equal(base.Content, artifact.Data) {
				return errors.New("derived merge base did not converge")
			}
		}
	}
	return nil
}

func hashDerivedManifest(artifacts []ledger.DerivedArtifact, semantic bool) string {
	hash := sha256.New()
	writeDerivedHashValue(hash, "session-reviewer-derived-v1")
	for _, artifact := range artifacts {
		writeDerivedHashValue(hash, artifact.RelativePath)
		writeDerivedHashValue(hash, artifact.EntityID)
		value := syncdoc.ContentHash(artifact.Data)
		if semantic && artifact.EntityID != "" {
			relative := strings.TrimPrefix(artifact.RelativePath, "docs/session-review/")
			if document, err := syncdoc.Parse(relative, artifact.Data); err == nil {
				value = hashSemanticUnits(document.SemanticUnits())
			}
		}
		writeDerivedHashValue(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashSemanticUnits(units syncdoc.UnitSet) string {
	keys := make([]syncdoc.UnitKey, 0, len(units))
	for key := range units {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Kind != keys[j].Kind {
			return keys[i].Kind < keys[j].Kind
		}
		return keys[i].Name < keys[j].Name
	})
	hash := sha256.New()
	for _, key := range keys {
		unit := units[key]
		writeDerivedHashValue(hash, string(key.Kind))
		writeDerivedHashValue(hash, key.Name)
		writeDerivedHashBytes(hash, unit.Value)
		writeDerivedHashBytes(hash, unit.KeyPresentation)
		writeDerivedHashBytes(hash, unit.HeadingPresentation)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeDerivedHashValue(writer io.Writer, value string) {
	writeDerivedHashBytes(writer, []byte(value))
}

func writeDerivedHashBytes(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}
