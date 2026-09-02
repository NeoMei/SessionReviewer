package reviewjob

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/agent/codex"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

// AgentHandle is an opaque proof that one concrete provider Adapter and
// executable passed the production verifier. Its fields are deliberately
// sealed: Run never accepts caller-authored capability booleans.
type AgentHandle struct {
	adapter            agent.Adapter
	capability         agent.Capability
	executable         string
	executableInfo     os.FileInfo
	executableIdentity pathguard.IdentityToken
	executableDigest   [sha256.Size]byte
}

// VerifyAgent is the production control-plane factory. Unsupported providers
// and unreviewed executable versions fail closed before an AgentHandle exists.
func VerifyAgent(ctx context.Context, provider, executable string) (*AgentHandle, error) {
	if ctx == nil {
		return nil, errors.New("Agent verification context is required")
	}
	var adapter agent.Adapter
	switch strings.TrimSpace(provider) {
	case "codex":
		adapter = codex.New()
	default:
		return nil, agent.NewError(agent.CodeUnconfigured, errors.New("unsupported Agent provider"))
	}
	physical, info, identity, digest, err := inspectAgentExecutable(executable)
	if err != nil {
		return nil, agent.NewError(agent.CodeUnconfigured, err)
	}
	capability, err := adapter.Verify(ctx, physical)
	if err != nil {
		return nil, err
	}
	if err := validateCapabilityContract(capability); err != nil {
		return nil, agent.NewError(agent.CodeIncompatible, err)
	}
	if capability.Provider != strings.TrimSpace(provider) {
		return nil, agent.NewError(agent.CodeIncompatible, errors.New("verified provider identity changed"))
	}
	if err := recheckAgentExecutable(physical, info, identity, digest); err != nil {
		return nil, agent.NewError(agent.CodeIncompatible, err)
	}
	return &AgentHandle{
		adapter: adapter, capability: capability, executable: physical,
		executableInfo: info, executableIdentity: identity, executableDigest: digest,
	}, nil
}

// VerifiedAgent returns only immutable identity metadata suitable for the
// private durable Job. It does not expose or reconstruct Capability.
func (handle *AgentHandle) VerifiedAgent() (VerifiedAgent, error) {
	if handle == nil || handle.adapter == nil || handle.executable == "" || !handle.executableIdentity.Valid() {
		return VerifiedAgent{}, errors.New("verified Agent handle is unavailable")
	}
	if err := validateCapabilityContract(handle.capability); err != nil {
		return VerifiedAgent{}, err
	}
	return VerifiedAgent{
		Kind: handle.capability.Provider, Identity: handle.executableIdentity,
		Version: handle.capability.Version, Executable: handle.executable,
	}, nil
}

func (handle *AgentHandle) validateFor(job Job) error {
	verified, err := handle.VerifiedAgent()
	if err != nil {
		return err
	}
	if verified != job.Agent {
		return errors.New("verified Agent handle does not match the frozen job")
	}
	return recheckAgentExecutable(handle.executable, handle.executableInfo, handle.executableIdentity, handle.executableDigest)
}

func (handle *AgentHandle) generate(ctx context.Context, request agent.Request) (agent.Result, error) {
	if handle == nil || handle.adapter == nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, errors.New("verified Agent handle is unavailable"))
	}
	// Recheck immediately before dispatch, independently of provider-internal
	// checks, so the opaque proof cannot outlive executable replacement.
	if err := recheckAgentExecutable(handle.executable, handle.executableInfo, handle.executableIdentity, handle.executableDigest); err != nil {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, err)
	}
	return handle.adapter.GenerateProposal(ctx, request)
}

func (handle *AgentHandle) cancel(ctx context.Context) error {
	if handle == nil || handle.adapter == nil {
		return agent.NewError(agent.CodeUnconfigured, errors.New("verified Agent handle is unavailable"))
	}
	return handle.adapter.Cancel(ctx)
}

func validateCapabilityContract(capability agent.Capability) error {
	if strings.TrimSpace(capability.Provider) == "" || strings.TrimSpace(capability.Version) == "" ||
		!capability.ProposalOnly || !capability.ReadOnly ||
		capability.Containment != agent.ContainmentRestrictedReadOnly ||
		!capability.StructuredOutput || !capability.NativeCancellation ||
		capability.ModelProvenance != agent.ModelProvenanceUnavailable {
		return errors.New("verified Agent capability does not satisfy the proposal-only restricted contract")
	}
	return nil
}

func inspectAgentExecutable(path string) (string, os.FileInfo, pathguard.IdentityToken, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if !filepath.IsAbs(path) {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("Agent executable must be absolute")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("Agent executable cannot be resolved")
	}
	physical = filepath.Clean(physical)
	before, err := os.Lstat(physical)
	if err != nil || !before.Mode().IsRegular() {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("Agent executable is not a regular file")
	}
	file, err := os.Open(physical)
	if err != nil {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("open Agent executable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("Agent executable changed while opening")
	}
	identity, err := pathguard.PhysicalFileIdentity(file)
	if err != nil {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("measure Agent executable identity")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("measure Agent executable digest")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	after, err := os.Lstat(physical)
	if err != nil || !os.SameFile(before, after) || after.Size() != opened.Size() || after.ModTime() != opened.ModTime() {
		return "", nil, pathguard.IdentityToken{}, zero, errors.New("Agent executable changed while measuring")
	}
	return physical, opened, identity, digest, nil
}

func recheckAgentExecutable(path string, expectedInfo os.FileInfo, expectedIdentity pathguard.IdentityToken, expectedDigest [sha256.Size]byte) error {
	physical, info, identity, digest, err := inspectAgentExecutable(path)
	if err != nil || physical != path || expectedInfo == nil || !os.SameFile(expectedInfo, info) ||
		identity != expectedIdentity || digest != expectedDigest {
		return fmt.Errorf("verified Agent executable identity changed")
	}
	return nil
}
