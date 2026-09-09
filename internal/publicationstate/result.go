package publicationstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

// MarkdownResultFingerprint identifies the accepted output, independently of
// preimages and timestamps. It is a locator, never itself acceptance proof.
func MarkdownResultFingerprint(projectID, generationID, projectViewDigest string, guard *IndexGuard, destinations []Destination) (string, error) {
	if !idPattern.MatchString(projectID) || !idPattern.MatchString(generationID) || !digestPattern.MatchString(projectViewDigest) || len(destinations) == 0 {
		return "", errors.New("invalid Markdown result identity")
	}
	if err := validateGuard(guard, generationID); err != nil {
		return "", err
	}
	values := cloneDestinations(destinations)
	for i := range values {
		values[i].PreimageSHA256 = ""
		values[i].PreimageExists = false
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Side != values[j].Side {
			return values[i].Side < values[j].Side
		}
		return values[i].Relative < values[j].Relative
	})
	if err := validateDestinations(values); err != nil {
		return "", err
	}
	body, err := canonical(struct {
		Version                                    int
		ProjectID, GenerationID, ProjectViewDigest string
		IndexGuard                                 *IndexGuard
		Destinations                               []Destination
	}{1, projectID, generationID, projectViewDigest, guard, values})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func resultFingerprint(receipt AcceptedReceipt) (string, error) {
	if err := ValidateReceipt(receipt, receipt.ProjectID); err != nil {
		return "", err
	}
	return MarkdownResultFingerprint(receipt.ProjectID, receipt.GenerationID, receipt.ProjectViewDigest, receipt.IndexGuard, receipt.Destinations)
}
func resultLeaf(fingerprint string) (string, error) {
	if !digestPattern.MatchString(fingerprint) {
		return "", errors.New("invalid accepted result fingerprint")
	}
	return "accepted-result-" + strings.TrimPrefix(fingerprint, "sha256:") + ".json", nil
}

// writeAcceptedResult runs only AFTER the latest receipt commit point and
// BEFORE the operational intent becomes terminal. Recovery fills any gap.
func writeAcceptedResult(root *os.Root, receipt AcceptedReceipt) error {
	fingerprint, err := resultFingerprint(receipt)
	if err != nil {
		return err
	}
	leaf, _ := resultLeaf(fingerprint)
	if _, err := ReadAcceptedResult(root, receipt.ProjectID, fingerprint); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := canonical(receipt)
	if err != nil {
		return err
	}
	if len(body) > maxStateBytes {
		return errors.New("accepted result exceeds size limit")
	}
	if err := atomicfile.WriteRootFileCreateIfAbsent(root, leaf, body, 0o600, nil); err != nil {
		return err
	}
	_, err = ReadAcceptedResult(root, receipt.ProjectID, fingerprint)
	return err
}

// ReadAcceptedResult authenticates an immutable historical receipt. It never
// substitutes the current document or a merely prepared publication intent.
func ReadAcceptedResult(root *os.Root, projectID, fingerprint string) (AcceptedReceipt, error) {
	leaf, err := resultLeaf(fingerprint)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	body, found, err := readPrivate(root, leaf)
	if err != nil {
		return AcceptedReceipt{}, err
	}
	if !found {
		return AcceptedReceipt{}, os.ErrNotExist
	}
	var receipt AcceptedReceipt
	if err := decode(body, &receipt); err != nil {
		return AcceptedReceipt{}, err
	}
	if err := ValidateReceipt(receipt, projectID); err != nil {
		return AcceptedReceipt{}, err
	}
	actual, err := resultFingerprint(receipt)
	if err != nil || actual != fingerprint {
		return AcceptedReceipt{}, errors.New("accepted result fingerprint mismatch")
	}
	return receipt, nil
}

func (r *Reader) AcceptedMarkdownResult(fingerprint string) (AcceptedReceipt, error) {
	if r == nil || r.journal == nil {
		return AcceptedReceipt{}, errors.New("publication reader is closed")
	}
	return ReadAcceptedResult(r.journal.Root, r.projectID, fingerprint)
}
