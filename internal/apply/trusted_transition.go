package apply

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type receiptContentState struct {
	exists bool
	digest string
}

// TrustsAppliedTransition reports whether valid applied-proposal receipts form
// an unbroken, forward-only chain from the synchronized preimage to the exact
// current Project document. It is read-only and fails closed when receipt state
// is malformed, redirected, over budget, or changes during inspection.
func TrustsAppliedTransition(projectData *os.Root, projectID, relativePath string, preimageExists bool, preimageHash, targetHash string) (bool, error) {
	if projectData == nil || !safeIdentifier(projectID) || !validReceiptRelativePath(relativePath) {
		return false, errors.New("invalid trusted apply transition input")
	}
	start, err := receiptStateFromContentHash(preimageExists, preimageHash)
	if err != nil {
		return false, err
	}
	target, err := receiptStateFromContentHash(true, targetHash)
	if err != nil {
		return false, err
	}
	if start == target {
		return false, nil
	}
	receipts, err := loadAppliedReceiptsReadOnly(projectData, projectID)
	if err != nil {
		return false, err
	}
	edges := make(map[receiptContentState][]receiptContentState)
	for _, receipt := range receipts {
		if receipt.State != receiptApplied {
			continue
		}
		changed := make(map[string]struct{}, len(receipt.ChangedFiles))
		for _, name := range receipt.ChangedFiles {
			changed[name] = struct{}{}
		}
		for _, file := range receipt.Files {
			if file.RelativePath != relativePath {
				continue
			}
			if _, ok := changed[file.RelativePath]; !ok {
				continue
			}
			from := receiptContentState{exists: file.PreimageExists, digest: file.PreimageSHA256}
			to := receiptContentState{exists: true, digest: file.TargetSHA256}
			edges[from] = append(edges[from], to)
		}
	}
	queue := []receiptContentState{start}
	visited := map[receiptContentState]struct{}{start: {}}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range edges[current] {
			if next == target {
				return true, nil
			}
			if _, seen := visited[next]; seen {
				continue
			}
			visited[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	return false, nil
}

func receiptStateFromContentHash(exists bool, contentHash string) (receiptContentState, error) {
	if !exists {
		if contentHash != "" {
			return receiptContentState{}, errors.New("missing trusted apply preimage has a digest")
		}
		return receiptContentState{}, nil
	}
	digest := "sha256:" + contentHash
	if !validReceiptDigest(digest) {
		return receiptContentState{}, errors.New("invalid trusted apply content digest")
	}
	return receiptContentState{exists: true, digest: digest}, nil
}

func loadAppliedReceiptsReadOnly(projectData *os.Root, projectID string) ([]applyReceipt, error) {
	directory, found, err := openReceiptChildReadOnly(projectData, "applied-proposals", "apply receipt directory")
	if err != nil || !found {
		return nil, err
	}
	defer directory.Close()
	directoryInfo, err := directory.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect apply receipt directory: %w", err)
	}
	file, err := directory.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := file.ReadDir(maxReceiptScanFiles + 1)
	closeErr := file.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("enumerate apply receipts: %w", err)
	}
	if len(entries) > maxReceiptScanFiles {
		return nil, fmt.Errorf("apply receipt count exceeds %d", maxReceiptScanFiles)
	}

	scanned := make([]scannedReceipt, 0, len(entries))
	folded := make(map[string]string, len(entries))
	var aggregate uint64
	for _, entry := range entries {
		name := entry.Name()
		fold := strings.ToLower(name)
		if prior, exists := folded[fold]; exists && prior != name {
			return nil, fmt.Errorf("case-colliding apply receipt names %q and %q", prior, name)
		}
		folded[fold] = name
		if _, err := receiptDigestFromName(name); err != nil {
			return nil, err
		}
		info, err := directory.Lstat(name)
		if err != nil {
			return nil, fmt.Errorf("inspect apply receipt: %w", err)
		}
		if !info.Mode().IsRegular() || isApplyRedirect(info) {
			return nil, fmt.Errorf("apply receipt %q is redirected or not regular", name)
		}
		if info.Size() < 0 || info.Size() > maxReceiptBytes {
			return nil, fmt.Errorf("apply receipt %q exceeds size limit", name)
		}
		size := uint64(info.Size())
		if aggregate > ^uint64(0)-size {
			return nil, errors.New("apply receipt aggregate size overflow")
		}
		aggregate += size
		scanned = append(scanned, scannedReceipt{name: name, info: info})
	}
	if aggregate > maxReceiptScanBytes {
		return nil, fmt.Errorf("apply receipt aggregate size exceeds %d bytes", maxReceiptScanBytes)
	}

	sort.Slice(scanned, func(i, j int) bool { return scanned[i].name < scanned[j].name })
	result := make([]applyReceipt, 0, len(scanned))
	remaining := uint64(maxReceiptScanBytes)
	for _, item := range scanned {
		digest, _ := receiptDigestFromName(item.name)
		receipt, err := loadReceiptRootWithMode(directory, item.name, digest, &remaining, nil, false)
		if err != nil {
			return nil, fmt.Errorf("scan apply receipt %q: %w", item.name, err)
		}
		if receipt.ProjectID != projectID {
			return nil, fmt.Errorf("apply receipt %q belongs to a different project", item.name)
		}
		if currentInfo, err := directory.Lstat(item.name); err != nil || !os.SameFile(item.info, currentInfo) {
			return nil, fmt.Errorf("apply receipt %q changed during scan", item.name)
		}
		result = append(result, receipt)
	}
	if err := verifyReceiptDirectorySnapshot(directory, scanned); err != nil {
		return nil, err
	}
	if err := validateReceiptDirectoryIdentity(projectData, directory, directoryInfo); err != nil {
		return nil, err
	}
	return result, nil
}
