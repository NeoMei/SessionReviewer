package sync

import (
	"bytes"

	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

type V4MergeInput struct {
	Base, Project, Vault syncdoc.UnitSet
	HasBase              bool
}

type V4MergeResult struct {
	Units     syncdoc.UnitSet
	Conflicts []UnitConflict
}

func MergeV4Units(input V4MergeInput) V4MergeResult {
	if input.HasBase {
		units, conflicts := mergeUnitSetsWithEqual(input.Base, input.Project, input.Vault, v4UnitsEqual)
		return V4MergeResult{Units: units, Conflicts: conflicts}
	}
	result := V4MergeResult{Units: syncdoc.UnitSet{}, Conflicts: []UnitConflict{}}
	for _, key := range sortedUnitKeys(input.Project, input.Vault) {
		project, vault := input.Project[key], input.Vault[key]
		if !v4UnitsEqual(project, vault) {
			result.Conflicts = append(result.Conflicts, UnitConflict{Key: key, Project: cloneUnit(project), Vault: cloneUnit(vault)})
			continue
		}
		if project.Present {
			result.Units[key] = cloneUnit(project)
		}
	}
	return result
}

func v4UnitsEqual(first, second syncdoc.Unit) bool {
	return first.Present == second.Present &&
		bytes.Equal(normalizeV4LineEndings(first.Value), normalizeV4LineEndings(second.Value)) &&
		bytes.Equal(first.KeyPresentation, second.KeyPresentation) &&
		bytes.Equal(first.HeadingPresentation, second.HeadingPresentation)
}

func normalizeV4LineEndings(value []byte) []byte {
	return bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
}
