// Package projectprobe records bounded, read-only live project state without
// executing project code or persisting file and command output bodies.
package projectprobe

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
)

const ProbeVersion = "project-probe-v1"

type Options struct {
	Binding                 projectidentity.Binding
	VersionFiles            []string
	RequiredProjectionFiles []string
	Now                     func() time.Time
	RunGit                  func(context.Context, string, ...string) ([]byte, error)
	GitExecutable           string
}

// Run authenticates the project root, probes only allowlisted read-only Git
// facts, and hashes explicitly declared regular files through a rooted handle.
func Run(ctx context.Context, options Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
	if err := ctx.Err(); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	versionPaths, requiredPaths, err := validateDeclaredPaths(options.VersionFiles, options.RequiredProjectionFiles)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	directory, err := authenticateBinding(options.Binding)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	defer directory.Close()

	runner := options.RunGit
	var gitExecutable *authenticatedGitExecutable
	if runner == nil {
		gitExecutable, err = authenticateGitExecutable(options.GitExecutable, directory)
		if err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		defer gitExecutable.Close()
		runner = defaultGitRunner(options.Binding.CanonicalRoot, gitExecutable)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	state := memory.ProjectProbeState{
		SchemaVersion:           memory.MemorySchemaVersion,
		ProjectID:               options.Binding.ProjectID,
		CanonicalRoot:           options.Binding.CanonicalRoot,
		RemoteIdentityHashes:    []string{},
		VersionFiles:            []memory.ProbeFile{},
		RequiredProjectionFiles: []memory.ProbeFile{},
		ProbeVersion:            ProbeVersion,
		Diagnostics:             []memory.Diagnostic{},
	}

	topLevel, err := runApprovedGit(ctx, runner, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, fmt.Errorf("authenticate Git top-level: %w", preserveCancellation(ctx, err))
	}
	topLevelPath, err := parseTopLevel(topLevel)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, errors.New("authenticate Git top-level: malformed output")
	}
	if err := authenticateGitTopLevel(topLevelPath, options.Binding.RootIdentity); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}

	branchOutput, branchErr := runApprovedGit(ctx, runner, "git", "symbolic-ref", "--short", "-q", "HEAD")
	if branchErr != nil {
		if err := ctx.Err(); err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		state.Diagnostics = append(state.Diagnostics, diagnostic("git_branch_unavailable", "", nil))
	} else if branch, parseErr := parseBranch(branchOutput); parseErr != nil {
		state.Diagnostics = append(state.Diagnostics, diagnostic("git_branch_malformed", "", branchOutput))
	} else {
		state.Branch = branch
	}

	headOutput, headErr := runApprovedGit(ctx, runner, "git", "rev-parse", "HEAD")
	if headErr != nil {
		if err := ctx.Err(); err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		state.Diagnostics = append(state.Diagnostics, diagnostic("git_head_unavailable", "", nil))
	} else if head, parseErr := parseHead(headOutput); parseErr != nil {
		state.Diagnostics = append(state.Diagnostics, diagnostic("git_head_malformed", "", headOutput))
	} else {
		state.Head = head
	}

	statusOutput, statusErr := runApprovedGit(ctx, runner, "git", "status", "--porcelain=v1", "-z")
	if statusErr != nil {
		if err := ctx.Err(); err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		state.Diagnostics = append(state.Diagnostics, diagnostic("git_status_unavailable", "", nil))
	} else {
		count, malformed := parseStatus(statusOutput)
		state.DirtyPathCount = count
		if malformed {
			state.Diagnostics = append(state.Diagnostics, diagnostic("git_status_malformed", "", statusOutput))
		}
	}

	remoteOutput, remoteErr := runApprovedGit(ctx, runner, "git", "remote", "get-url", "--all", "origin")
	if remoteErr != nil {
		if err := ctx.Err(); err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		state.Diagnostics = append(state.Diagnostics, diagnostic("git_remote_unavailable", "", nil))
	} else {
		remotes, malformed, excess := parseRemoteIdentities(remoteOutput)
		state.RemoteIdentityHashes = remotes
		if malformed {
			state.Diagnostics = append(state.Diagnostics, diagnostic("git_remote_malformed", "", remoteOutput))
		}
		if excess {
			state.Diagnostics = append(state.Diagnostics, diagnostic("git_remote_excess", "", nil))
		}
	}

	state.VersionFiles, state.Diagnostics, err = probeFiles(ctx, directory, versionPaths, state.Diagnostics)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	state.RequiredProjectionFiles, state.Diagnostics, err = probeFiles(ctx, directory, requiredPaths, state.Diagnostics)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	if err := projectidentity.Reauthenticate(options.Binding); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	if err := ctx.Err(); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	state.Diagnostics = canonicalDiagnostics(state.Diagnostics)
	digest, err := memory.ProjectProbeStateDigest(state)
	if err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, fmt.Errorf("digest project probe state: %w", err)
	}
	state.Digest = digest
	if err := memory.ValidateProjectProbeState(state); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, fmt.Errorf("invalid project probe state: %w", err)
	}
	check := memory.ProbeCheck{
		SchemaVersion: memory.MemorySchemaVersion,
		CheckedAt:     now().UTC().Format(time.RFC3339Nano),
		StateDigest:   state.Digest,
		Available:     len(state.Diagnostics) == 0,
		Diagnostics:   cloneDiagnostics(state.Diagnostics),
	}
	if err := ctx.Err(); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
	}
	if err := memory.ValidateProbeCheck(check); err != nil {
		return memory.ProjectProbeState{}, memory.ProbeCheck{}, fmt.Errorf("invalid probe check: %w", err)
	}
	return cloneState(state), check, nil
}

func authenticateBinding(binding projectidentity.Binding) (*pathguard.Directory, error) {
	if binding.ProjectID == "" || binding.CanonicalRoot == "" || !filepath.IsAbs(binding.CanonicalRoot) || filepath.Clean(binding.CanonicalRoot) != binding.CanonicalRoot || !binding.RootIdentity.Valid() {
		return nil, errors.New("authenticated project binding is invalid")
	}
	alias := binding.AuthenticatedAlias
	if alias.SchemaVersion != 1 || alias.RootIdentity != binding.RootIdentity || alias.CommonDirIdentity != binding.CommonDirIdentity {
		return nil, errors.New("authenticated project alias does not match binding")
	}
	if err := projectidentity.Reauthenticate(binding); err != nil {
		return nil, fmt.Errorf("reauthenticate project binding: %w", err)
	}
	directory, err := pathguard.Open(binding.CanonicalRoot)
	if err != nil {
		return nil, fmt.Errorf("open authenticated project root: %w", err)
	}
	identity, err := directory.PhysicalIdentity()
	if err != nil || identity != binding.RootIdentity {
		_ = directory.Close()
		return nil, errors.New("authenticated project root identity changed")
	}
	return directory, nil
}

func authenticateGitTopLevel(topLevel string, expected pathguard.IdentityToken) error {
	directory, err := pathguard.Open(topLevel)
	if err != nil {
		return errors.New("Git top-level is unavailable or redirected")
	}
	defer directory.Close()
	identity, err := directory.PhysicalIdentity()
	if err != nil || identity != expected {
		return errors.New("Git top-level does not match authenticated project root")
	}
	return nil
}

func diagnostic(code, path string, detail []byte) memory.Diagnostic {
	value := memory.Diagnostic{Code: code, Path: path}
	if len(detail) > 0 {
		sum := sha256.Sum256(detail)
		value.DetailHash = fmt.Sprintf("sha256:%x", sum)
	}
	return value
}

func canonicalDiagnostics(values []memory.Diagnostic) []memory.Diagnostic {
	sort.Slice(values, func(i, j int) bool {
		left := values[i].Code + "\x00" + values[i].Path + "\x00" + values[i].DetailHash
		right := values[j].Code + "\x00" + values[j].Path + "\x00" + values[j].DetailHash
		return left < right
	})
	result := make([]memory.Diagnostic, 0, len(values))
	for _, value := range values {
		if len(result) > 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}

func cloneDiagnostics(values []memory.Diagnostic) []memory.Diagnostic {
	result := make([]memory.Diagnostic, len(values))
	copy(result, values)
	return result
}

func cloneState(value memory.ProjectProbeState) memory.ProjectProbeState {
	value.RemoteIdentityHashes = cloneStrings(value.RemoteIdentityHashes)
	value.VersionFiles = cloneProbeFiles(value.VersionFiles)
	value.RequiredProjectionFiles = cloneProbeFiles(value.RequiredProjectionFiles)
	value.Diagnostics = cloneDiagnostics(value.Diagnostics)
	return value
}

func cloneStrings(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func cloneProbeFiles(values []memory.ProbeFile) []memory.ProbeFile {
	result := make([]memory.ProbeFile, len(values))
	copy(result, values)
	return result
}

func preserveCancellation(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func parseTopLevel(output []byte) (string, error) {
	value, err := parseSingleLine(output, 4096)
	if err != nil || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", errors.New("invalid Git top-level")
	}
	return value, nil
}
