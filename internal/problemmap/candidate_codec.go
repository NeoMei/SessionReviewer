package problemmap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func ParseCandidates(data []byte) (CandidateStore, error) {
	var store CandidateStore
	if err := strictjson.Decode(data, &store); err != nil {
		return store, err
	}
	if err := ValidateCandidates(store); err != nil {
		return store, strictjson.NewRejection(strictjson.CodeContractInvalid, err)
	}
	if !isZeroDigest(store.Digest) && CanonicalDigest(store) != store.Digest {
		return store, strictjson.NewRejection(strictjson.CodeContractInvalid, errors.New("problem candidate store digest mismatch"))
	}
	return store, nil
}

func RenderCandidates(store CandidateStore) ([]byte, error) {
	normalizeCandidates(&store)
	store.Digest = zeroDigest()
	if err := ValidateCandidates(store); err != nil {
		return nil, err
	}
	store.Digest = CanonicalDigest(store)
	body, err := strictjson.Encode(store)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseCandidates(body)
	if err != nil {
		return nil, fmt.Errorf("rendered problem candidates failed validation: %w", err)
	}
	if !reflect.DeepEqual(store, parsed) {
		return nil, errors.New("rendered problem candidates changed semantic value")
	}
	return body, nil
}

func CanonicalDigest(store CandidateStore) string {
	body := struct {
		SchemaVersion        int         `json:"schema_version"`
		MinimumReaderVersion string      `json:"minimum_reader_version"`
		ProjectID            string      `json:"project_id"`
		Candidates           []Candidate `json:"candidates"`
	}{store.SchemaVersion, store.MinimumReaderVersion, store.ProjectID, store.Candidates}
	encoded, err := strictjson.Encode(body)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalizeCandidates(store *CandidateStore) {
	if store.Candidates == nil {
		store.Candidates = []Candidate{}
	}
	for index := range store.Candidates {
		candidate := &store.Candidates[index]
		if candidate.SourceTurnRefs == nil {
			candidate.SourceTurnRefs = []reviewv4.SourceTurnRef{}
		}
		if candidate.AlternateTargetIDs == nil {
			candidate.AlternateTargetIDs = []string{}
		}
		if candidate.RelatedNodeIDs == nil {
			candidate.RelatedNodeIDs = []string{}
		}
		if candidate.Grounds == nil {
			candidate.Grounds = []Ground{}
		}
		if candidate.DependencyDigests == nil {
			candidate.DependencyDigests = []string{}
		}
		for groundIndex := range candidate.Grounds {
			if candidate.Grounds[groundIndex].MatchedFactRefs == nil {
				candidate.Grounds[groundIndex].MatchedFactRefs = []string{}
			}
		}
	}
}

func zeroDigest() string             { return "sha256:" + strings.Repeat("0", 64) }
func isZeroDigest(value string) bool { return value == zeroDigest() }
