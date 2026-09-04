package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicationProofLegacyJSONBytesRemainUnchanged(t *testing.T) {
	proof := PublicationProof{
		ProjectID: "project-p", GenerationID: "generation-1", ManifestDigest: "sha256:" + strings.Repeat("1", 64), ProjectViewDigest: "sha256:" + strings.Repeat("2", 64),
		ReviewSHA256: strings.Repeat("3", 64), HistorySHA256: strings.Repeat("4", 64), LedgerSHA256: strings.Repeat("5", 64), JournalVerified: true,
	}
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"project_id":"project-p","generation_id":"generation-1","manifest_digest":"sha256:` + strings.Repeat("1", 64) + `","project_view_digest":"sha256:` + strings.Repeat("2", 64) + `","review_sha256":"` + strings.Repeat("3", 64) + `","history_sha256":"` + strings.Repeat("4", 64) + `","ledger_sha256":"` + strings.Repeat("5", 64) + `","journal_verified":true}`
	if string(body) != want {
		t.Fatalf("legacy proof bytes changed:\n got %s\nwant %s", body, want)
	}
}
