package projectprobe

import (
	"context"
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
	maxDeclaredProbeFiles = 4091
)

func validateDeclaredPaths(versionFiles, requiredFiles []string) ([]string, []string, error) {
	if len(versionFiles)+len(requiredFiles) > maxDeclaredProbeFiles {
		return nil, nil, errors.New("too many declared probe files")
	}
	seen := make(map[string]struct{}, len(versionFiles)+len(requiredFiles))
	validate := func(values []string) ([]string, error) {
		result := append([]string(nil), values...)
		for _, value := range result {
			if len(value) > 1024 || strings.Contains(value, `\`) {
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

func probeFiles(ctx context.Context, directory *pathguard.Directory, paths []string, diagnostics []memory.Diagnostic) ([]memory.ProbeFile, []memory.Diagnostic, error) {
	return probeFilesWithReader(ctx, paths, diagnostics, directory.ReadRegularOptional)
}

func probeFilesWithReader(ctx context.Context, paths []string, diagnostics []memory.Diagnostic, read func(string, int64) ([]byte, bool, error)) ([]memory.ProbeFile, []memory.Diagnostic, error) {
	if ctx == nil || read == nil {
		return nil, nil, errors.New("probe file context and reader are required")
	}
	result := make([]memory.ProbeFile, 0, len(paths))
	for _, relative := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		body, found, err := read(relative, maxProbeFileBytes)
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, contextErr
		}
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
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return result, diagnostics, nil
}
