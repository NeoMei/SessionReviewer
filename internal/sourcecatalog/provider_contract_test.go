package sourcecatalog

import "testing"

func TestProviderContractCatalogKeepsSameNativeIDNamespaced(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	for _, provider := range []string{"codex", "claude"} {
		record := sourceRecord("same-native-id", []string{"project-a"}, 10)
		record.Provider = provider
		record.SourceIdentity = provider + "-source"
		if _, err := catalog.UpsertSource(record); err != nil {
			t.Fatalf("upsert %s source: %v", provider, err)
		}
	}
	for _, provider := range []string{"codex", "claude"} {
		record, found, err := catalog.GetSource(provider, "same-native-id")
		if err != nil || !found || record.Provider != provider {
			t.Fatalf("get %s source: record=%+v found=%v err=%v", provider, record, found, err)
		}
	}
}
