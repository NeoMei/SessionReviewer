package syncproject

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// markdownStatusReadSet records the actual authenticated builder inputs before
// merging, including on semantic conflict. It is not an acceptance capability.
type markdownStatusReadSet struct {
	privateRoots        []*pathguard.Directory
	project, vault      map[string]string
	intent              publicationstate.Intent
	receipt             publicationstate.AcceptedReceipt
	generation          string
	manifest            memory.GenerationManifest
	base                syncengine.BaseRecord
	viewHash, indexHash string
}

// StatusMarkdown observes one authenticated Markdown aggregate without locks,
// recovery, or publication. Invalid or changing inputs never yield a Status.
func StatusMarkdown(ctx context.Context, options Options) (_ syncengine.Status, retErr error) {
	if ctx == nil {
		return syncengine.Status{}, errors.New("sync context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return syncengine.Status{}, err
	}
	pin, err := PinMapping(options)
	if err != nil {
		return syncengine.Status{}, err
	}
	defer func() { retErr = errors.Join(retErr, pin.Close()) }()
	report := syncengine.Report{ProjectID: pin.mapping.ID}
	roots, err := pinMarkdownStatusRoots(pin)
	if err != nil {
		return syncengine.Status{}, err
	}
	defer func() {
		for _, root := range roots {
			retErr = errors.Join(retErr, root.Close())
		}
	}()
	readSet := markdownStatusReadSet{privateRoots: roots}
	plan, _, buildErr := buildMarkdownPlanWithReadSet(pin, options, &report, &readSet)
	if buildErr != nil && buildErr != errMarkdownFieldsConflict {
		return syncengine.Status{}, buildErr
	}
	if options.afterMarkdownBuild != nil {
		if err := options.afterMarkdownBuild(); err != nil {
			return syncengine.Status{}, err
		}
	}
	if err := readSet.verify(pin, options); err != nil {
		return syncengine.Status{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return syncengine.Status{}, err
	}
	status := syncengine.Status{
		ProjectID: pin.mapping.ID, OpenConflicts: append([]string{}, report.Conflicts...),
		Pending: markdownOperations(plan), PendingOperations: markdownOperations(plan), HiddenConflictIDs: []string{},
		Migration: "current", DerivedState: syncengine.DerivedCurrent, MachineState: syncengine.MachineCurrent,
	}
	switch {
	case len(report.Conflicts) != 0:
		status.Conflicted = len(report.Conflicts)
		status.DerivedState, status.MachineState = syncengine.DerivedDeferred, syncengine.MachineBlocked
	case len(status.Pending) != 0:
		status.DerivedState, status.MachineState = syncengine.DerivedPending, syncengine.MachinePending
	default:
		// Markdown stores one document-pair Base, not one Base per field.
		status.InSync = 1
	}
	return status, nil
}

func (readSet markdownStatusReadSet) verify(pin *MappingPin, options Options) error {
	const changed = "Markdown status inputs changed or are unavailable"
	if readSet.generation == "" {
		return errors.New(changed)
	}
	if err := pin.verify(options); err != nil {
		return err
	}
	if err := verifyMarkdownStatusRoots(readSet.privateRoots); err != nil {
		return err
	}
	state, err := publicationstate.OpenReadOnly(pin.data.Path, pin.mapping.ID)
	if err != nil {
		return errors.New(changed)
	}
	defer state.Close()
	intent, err := state.Intent()
	if err != nil || !reflect.DeepEqual(intent, readSet.intent) {
		return errors.New(changed)
	}
	receipt, err := state.Accepted()
	if err != nil || !reflect.DeepEqual(receipt, readSet.receipt) {
		return errors.New(changed)
	}
	store, err := memorystore.OpenReadOnly(pin.data.Path, pin.mapping.ID)
	if err != nil {
		return errors.New(changed)
	}
	defer store.Close()
	generation, manifest, err := store.LoadPublished()
	if err != nil || generation != readSet.generation || !reflect.DeepEqual(manifest, readSet.manifest) {
		return errors.New(changed)
	}
	view, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil || bareHash(view) != readSet.viewHash {
		return errors.New(changed)
	}
	index, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil || bareHash(index) != readSet.indexHash {
		return errors.New(changed)
	}
	base, found, err := (syncengine.BaseStore{Root: pin.syncData.Root}).Load(syncengine.MarkdownBaseEntityID)
	if err != nil || !found || !reflect.DeepEqual(base, readSet.base) {
		return errors.New(changed)
	}
	for _, side := range []struct {
		root   *pathguard.Directory
		prefix string
		hashes map[string]string
	}{
		{pin.project, "docs/session-review", readSet.project}, {pin.vault, pin.mapping.VaultReviewPath, readSet.vault},
	} {
		for relative, hash := range side.hashes {
			body, found, err := side.root.ReadRegularOptional(filepath.ToSlash(filepath.Join(side.prefix, relative)), 64<<20)
			if err != nil || !found || bareHash(body) != hash {
				return errors.New(changed)
			}
		}
	}
	// Publication may have started during the public-file checks. A second
	// committed-intent/receipt check closes that observation window.
	intent, err = state.Intent()
	if err != nil || !reflect.DeepEqual(intent, readSet.intent) {
		return errors.New(changed)
	}
	receipt, err = state.Accepted()
	if err != nil || !reflect.DeepEqual(receipt, readSet.receipt) {
		return errors.New(changed)
	}
	return errors.Join(verifyMarkdownStatusRoots(readSet.privateRoots), pin.verify(options))
}

// Keep physical anchors open while the builder reads, so even same-content
// replacement of the private namespaces is rejected by the final check.
func pinMarkdownStatusRoots(pin *MappingPin) ([]*pathguard.Directory, error) {
	base := filepath.Join("projects", pin.mapping.ID)
	var roots []*pathguard.Directory
	for _, relative := range []string{
		filepath.Join("publication-journal", pin.mapping.ID), filepath.Join(base, "merge-bases"),
		filepath.Join(base, "memory-v1"), filepath.Join(base, "memory-v1/generations"),
		filepath.Join(base, "memory-v1/project-views"), filepath.Join(base, "memory-v1/session-indexes"),
	} {
		root, err := pathguard.Open(filepath.Join(pin.data.Path, relative))
		if err == nil && !root.ContainsIdentity(pin.data.Info()) {
			err = errors.New("Markdown status private root escaped Data")
		}
		if err != nil {
			if root != nil {
				_ = root.Close()
			}
			for _, opened := range roots {
				_ = opened.Close()
			}
			return nil, errors.New("Markdown status private root is unavailable")
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func verifyMarkdownStatusRoots(roots []*pathguard.Directory) error {
	for _, root := range roots {
		current, err := pathguard.Open(root.Path)
		if err != nil {
			return errors.New("Markdown status private root changed")
		}
		same := os.SameFile(root.Info(), current.Info())
		if err := current.Close(); err != nil || !same {
			return errors.New("Markdown status private root changed")
		}
	}
	return nil
}
