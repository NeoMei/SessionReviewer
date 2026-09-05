package baselinehash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SHA256 hashes the exact ordered generated-baseline identity shared by
// presentation codecs. Its closed arguments keep business DTOs out of this
// lower-level package.
func SHA256(entityID, field, kind, value string, values []string) string {
	identity := struct {
		SchemaVersion int      `json:"schema_version"`
		EntityID      string   `json:"entity_id"`
		Field         string   `json:"field"`
		Kind          string   `json:"kind"`
		Value         string   `json:"value"`
		Values        []string `json:"values"`
	}{1, entityID, field, kind, value, values}
	body, err := json.Marshal(identity)
	if err != nil {
		panic("baselinehash: fixed canonical identity cannot fail JSON encoding: " + err.Error())
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
