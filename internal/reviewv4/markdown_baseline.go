package reviewv4

import (
	"errors"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
)

// CarryMarkdownGeneratedBaselines validates the authenticated baseline/patch
// metadata and advances only current-generation scalar baselines that still
// name a real field in the fixed Markdown registry. Historical metadata,
// including list baselines for removed entities, keeps its original binding.
func CarryMarkdownGeneratedBaselines(presentation *Presentation, generationID string) error {
	fields := newMarkdownPresentationIndex(presentation)
	baselines := make(map[string]int, len(presentation.GeneratedBaselines))
	live := make(map[string]bool, len(presentation.GeneratedBaselines))
	for index := range presentation.GeneratedBaselines {
		baseline := &presentation.GeneratedBaselines[index]
		key, isLive, err := fields.storedSemanticKey(baseline.EntityID, baseline.Field)
		if err != nil {
			return err
		}
		if _, duplicate := baselines[key]; duplicate || !validMarkdownBaselineShape(*baseline) {
			return errors.New("malformed or duplicate Markdown generated baseline")
		}
		baselines[key] = index
		if isLive {
			if baseline.Kind != "scalar" || baseline.GenerationID != presentation.GenerationID {
				return errors.New("live Markdown generated baseline is not bound to the authenticated generation")
			}
			live[key] = true
		}
	}

	patches := make(map[string]bool, len(presentation.HumanPatches)+len(presentation.OrphanPatches))
	for _, patch := range presentation.HumanPatches {
		key, isLive, err := fields.storedSemanticKey(patch.EntityID, patch.Field)
		if err != nil {
			return err
		}
		index, found := baselines[key]
		if patches[key] || !found || !isLive || !live[key] || !validMarkdownPatchBinding(patch, presentation.GeneratedBaselines[index]) {
			return errors.New("unbound or duplicate Markdown human patch")
		}
		patches[key] = true
	}
	for _, patch := range presentation.OrphanPatches {
		key, isLive, err := fields.storedSemanticKey(patch.EntityID, patch.Field)
		if err != nil {
			return err
		}
		index, found := baselines[key]
		if patches[key] || !found || isLive || live[key] || !validMarkdownPatchBinding(patch, presentation.GeneratedBaselines[index]) {
			return errors.New("unbound, live, or duplicate Markdown orphan patch")
		}
		patches[key] = true
	}
	for key := range live {
		presentation.GeneratedBaselines[baselines[key]].GenerationID = generationID
	}
	return nil
}

func validMarkdownBaselineShape(baseline Baseline) bool {
	switch baseline.Kind {
	case "scalar":
		return baseline.Value != nil && baseline.Values == nil &&
			baseline.GeneratedHash == baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, *baseline.Value, nil)
	case "list":
		return baseline.Value == nil && baseline.Values != nil &&
			baseline.GeneratedHash == baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, "", *baseline.Values)
	default:
		return false
	}
}

func validMarkdownPatchBinding(patch Patch, baseline Baseline) bool {
	if patch.EntityID != baseline.EntityID || patch.Field != baseline.Field || patch.BaseGeneratedHash != baseline.GeneratedHash {
		return false
	}
	switch patch.Operation {
	case "set":
		if baseline.Kind == "scalar" {
			return patch.Value != nil && patch.Values == nil
		}
		return baseline.Kind == "list" && patch.Value == nil && patch.Values != nil
	case "suppress", "restore_default":
		return patch.Value == nil && patch.Values == nil
	default:
		return false
	}
}
