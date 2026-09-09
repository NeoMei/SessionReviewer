package modelpricewatch

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveCatalogSmoke is opt-in so normal and CI tests never depend on the
// public service. It logs only aggregate adapter metadata, never catalog rows.
func TestLiveCatalogSmoke(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_LIVE_MODELPRICEWATCH") != "1" {
		t.Skip("set SESSION_REVIEWER_LIVE_MODELPRICEWATCH=1")
	}
	client := NewClient(&http.Client{Timeout: 30 * time.Second})
	result, err := client.Fetch(context.Background(), Validators{})
	if err != nil {
		t.Fatal(err)
	}
	if result.NotModified || result.Catalogs.Models.Count == 0 || result.Catalogs.Models.Count != result.Catalogs.History.Count {
		t.Fatalf("invalid aggregate shape: models=%d history=%d not_modified=%t", result.Catalogs.Models.Count, result.Catalogs.History.Count, result.NotModified)
	}
	t.Logf("adapter=%d models_count=%d history_count=%d models_updated=%s history_updated=%s", AdapterSchemaVersion, result.Catalogs.Models.Count, result.Catalogs.History.Count, result.Catalogs.Models.Updated, result.Catalogs.History.Updated)
}
