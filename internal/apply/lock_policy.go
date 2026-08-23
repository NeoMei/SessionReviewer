package apply

import (
	"crypto/sha256"
	"encoding/hex"
)

func windowsApplyMutexName(dataIdentity, projectID string) string {
	digest := sha256.Sum256([]byte(dataIdentity + "\x00" + projectID))
	return `Local\SessionReviewer.Apply.` + hex.EncodeToString(digest[:])
}
