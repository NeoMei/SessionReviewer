package memorystore

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
)

// PutConversationChain stores one canonical, immutable conversation-chain
// document in the existing project-scoped CAS.
func (s *Store) PutConversationChain(value conversationchain.Document) (string, error) {
	body, err := conversationchain.Render(value)
	if err != nil {
		return "", err
	}
	parsed, err := conversationchain.Parse(body)
	if err != nil {
		return "", err
	}
	if value.Digest != parsed.Digest {
		return "", errors.New("conversation chain digest does not match canonical body")
	}
	if parsed.ProjectID != s.projectID {
		return "", errors.New("conversation chain belongs to a different project")
	}
	viewBody, err := s.LoadObject(ObjectSessionView, parsed.SessionViewDigest)
	if err != nil {
		return "", errors.Join(errors.New("conversation chain SessionView is unavailable"), err)
	}
	var view struct {
		ProjectID string `json:"project_id"`
		Provider  string `json:"provider"`
		SessionID string `json:"session_id"`
		Digest    string `json:"digest"`
	}
	if err := json.Unmarshal(viewBody, &view); err != nil || view.ProjectID != parsed.ProjectID || view.Provider != parsed.Provider || view.SessionID != parsed.SessionID || view.Digest != parsed.SessionViewDigest {
		return "", errors.Join(errors.New("conversation chain SessionView identity mismatch"), err)
	}
	if err := s.putImmutable(ObjectConversationChain, parsed.Digest, body); err != nil {
		return "", err
	}
	return parsed.Digest, nil
}

func decodeConversationChain(body []byte, digest, projectID string) (conversationchain.Document, error) {
	value, err := conversationchain.Parse(body)
	if err != nil {
		return conversationchain.Document{}, err
	}
	canonical, err := conversationchain.Render(value)
	if err != nil {
		return conversationchain.Document{}, err
	}
	if !bytes.Equal(canonical, body) {
		return conversationchain.Document{}, errors.New("stored conversation chain is not canonical")
	}
	if value.Digest != digest || value.ProjectID != projectID {
		return conversationchain.Document{}, errors.New("invalid stored conversation chain identity")
	}
	return value, nil
}
