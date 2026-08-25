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

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

const derivedTransactionID = "derived-navigation"

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
