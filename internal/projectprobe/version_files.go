package projectprobe

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

const (
	maxProbeFileBytes     = 1 << 20
	maxDeclaredProbeFiles = 4092
)

func validateDeclaredPaths(versionFiles, requiredFiles []string) ([]string, []string, error) {
	if len(versionFiles)+len(requiredFiles) > maxDeclaredProbeFiles {
		return nil, nil, errors.New("too many declared probe files")
	}
	seen := make(map[string]struct{}, len(versionFiles)+len(requiredFiles))
	validate := func(values []string) ([]string, error) {
		result := append([]string(nil), values...)
		for _, value := range result {
			if strings.Contains(value, `\`) {
				return nil, errors.New("declared probe path must use slash separators")
			}
			key, err := platform.PathKey(runtime.GOOS, platform.CaseInsensitive, value)
			if err != nil {
				return nil, fmt.Errorf("invalid declared probe path: %w", err)
			}
			if _, duplicate := seen[key]; duplicate {
				return nil, errors.New("duplicate or aliased declared probe path")
			}
			seen[key] = struct{}{}
		}
		sort.Strings(result)
		return result, nil
	}
	versions, err := validate(versionFiles)
	if err != nil {
		return nil, nil, err
	}
	required, err := validate(requiredFiles)
	if err != nil {
		return nil, nil, err
	}
	return versions, required, nil
}

func probeFiles(directory *pathguard.Directory, paths []string, diagnostics []memory.Diagnostic) ([]memory.ProbeFile, []memory.Diagnostic) {
	result := make([]memory.ProbeFile, 0, len(paths))
	for _, relative := range paths {
		body, found, err := directory.ReadRegularOptional(relative, maxProbeFileBytes)
		file := memory.ProbeFile{Path: relative}
		if err != nil {
			diagnostics = append(diagnostics, diagnostic("probe_file_unavailable", relative, []byte(err.Error())))
			result = append(result, file)
			continue
		}
		if found {
			sum := sha256.Sum256(body)
			file.Exists = true
			file.ContentHash = fmt.Sprintf("%x", sum)
		}
		result = append(result, file)
	}
	return result, diagnostics
}
