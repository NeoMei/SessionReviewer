package conversationchain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func Parse(data []byte) (Document, error) {
	var document Document
	if err := strictjson.Decode(data, &document); err != nil {
		return document, err
	}
	if err := Validate(document); err != nil {
		return document, strictjson.NewRejection(strictjson.CodeContractInvalid, err)
	}
	if CanonicalDigest(document) != document.Digest {
		return document, strictjson.NewRejection(strictjson.CodeContractInvalid, errors.New("conversation chain digest mismatch"))
	}
	return document, nil
}

func Render(document Document) ([]byte, error) {
	normalize(&document)
	document.Digest = zeroDigest()
	if err := Validate(document); err != nil {
		return nil, err
	}
	document.Digest = CanonicalDigest(document)
	body, err := strictjson.Encode(document)
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("rendered conversation chain failed validation: %w", err)
	}
	if !reflect.DeepEqual(document, parsed) {
		return nil, errors.New("rendered conversation chain changed semantic value")
	}
	return body, nil
}

func CanonicalDigest(document Document) string {
	body := struct {
		SchemaVersion           int        `json:"schema_version"`
		MinimumReaderVersion    string     `json:"minimum_reader_version"`
		ProjectID               string     `json:"project_id"`
		Provider                string     `json:"provider"`
		SessionID               string     `json:"session_id"`
		SessionViewDigest       string     `json:"session_view_digest"`
		DependencyDigest        string     `json:"dependency_digest"`
		SegmentationRuleVersion string     `json:"segmentation_rule_version"`
		Coverage                Coverage   `json:"coverage"`
		TurnUnits               []TurnUnit `json:"turn_units"`
	}{document.SchemaVersion, document.MinimumReaderVersion, document.ProjectID, document.Provider, document.SessionID, document.SessionViewDigest, document.DependencyDigest, document.SegmentationRuleVersion, document.Coverage, document.TurnUnits}
	encoded, err := strictjson.Encode(body)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalize(document *Document) {
	if document.TurnUnits == nil {
		document.TurnUnits = []TurnUnit{}
	}
	for index := range document.TurnUnits {
		turn := &document.TurnUnits[index]
		if turn.AssistantMessages == nil {
			turn.AssistantMessages = []Message{}
		}
		if turn.Actions == nil {
			turn.Actions = []Action{}
		}
		if turn.Results == nil {
			turn.Results = []Result{}
		}
	}
}

func zeroDigest() string { return "sha256:" + strings.Repeat("0", 64) }
