package modelpricewatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fetchFunc func(context.Context, Validators) (FetchResult, error)

func (f fetchFunc) Fetch(ctx context.Context, v Validators) (FetchResult, error) { return f(ctx, v) }

func TestCacheSeparatesAttemptThrottleFromSuccessfulRetrieval(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	client := fetchFunc(func(context.Context, Validators) (FetchResult, error) {
		calls++
		return FetchResult{}, errors.New("offline")
	})
	cache, err := NewCache(filepath.Join(t.TempDir(), "pricing"), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = cache.LoadOrRefresh(context.Background(), now); err == nil {
		t.Fatal("first failure accepted")
	}
	if _, _, err = cache.LoadOrRefresh(context.Background(), now.Add(time.Hour)); err == nil || calls != 1 {
		t.Fatalf("throttled err=%v calls=%d", err, calls)
	}
	if _, _, err = cache.LoadOrRefresh(context.Background(), now.Add(24*time.Hour+time.Second)); err == nil || calls != 2 {
		t.Fatalf("retry err=%v calls=%d", err, calls)
	}
}

func TestCachePublishesValidatedPairAndUsesStaleFallback(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	fixture := fixtureFetch(t)
	calls := 0
	client := fetchFunc(func(context.Context, Validators) (FetchResult, error) {
		calls++
		if calls == 1 {
			return fixture, nil
		}
		return FetchResult{}, errors.New("offline")
	})
	root := filepath.Join(t.TempDir(), "pricing")
	cache, err := NewCache(root, client)
	if err != nil {
		t.Fatal(err)
	}
	set, fresh, err := cache.LoadOrRefresh(context.Background(), now)
	if err != nil || fresh.Status != FreshCurrent || set.Models.Count != 1 {
		t.Fatalf("set=%#v fresh=%#v err=%v", set, fresh, err)
	}
	set, fresh, err = cache.LoadOrRefresh(context.Background(), now.Add(25*time.Hour))
	if err != nil || fresh.Status != FreshStale || fresh.LastRefreshError == "" || set.History.Count != 1 {
		t.Fatalf("stale=%#v set=%#v err=%v", fresh, set, err)
	}
	if fresh.RetrievedAt != now || fresh.AttemptedAt != now.Add(25*time.Hour) {
		t.Fatalf("times conflated: %#v", fresh)
	}
	if _, fresh, err = cache.LoadOrRefresh(context.Background(), now.Add(8*24*time.Hour)); err == nil || fresh.Status != FreshExpired {
		t.Fatalf("expired=%#v err=%v", fresh, err)
	}
	for _, name := range []string{"active.json", "sets"} {
		info, statErr := os.Stat(filepath.Join(root, name))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if (name == "sets" && !info.IsDir()) || (name == "active.json" && !info.Mode().IsRegular()) {
			t.Fatalf("%s type=%v", name, info.Mode())
		}
		// Windows FileMode does not describe access-control permissions.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode=%o", name, info.Mode().Perm())
		}
	}
}

func TestCacheRejectsMismatchedPairWithoutReplacingCurrent(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	good := fixtureFetch(t)
	calls := 0
	client := fetchFunc(func(context.Context, Validators) (FetchResult, error) {
		calls++
		if calls == 1 {
			return good, nil
		}
		bad := good
		bad.Catalogs.History.Models = map[string]ModelHistory{"other": bad.Catalogs.History.Models["provider-model-test"]}
		return bad, nil
	})
	cache, err := NewCache(filepath.Join(t.TempDir(), "pricing"), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = cache.LoadOrRefresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	set, fresh, err := cache.LoadOrRefresh(context.Background(), now.Add(25*time.Hour))
	if err != nil || fresh.Status != FreshStale || set.Models.Listings["provider-model-test"].Model != "Model Test" {
		t.Fatalf("set=%#v fresh=%#v err=%v", set, fresh, err)
	}
}

func fixtureFetch(t *testing.T) FetchResult {
	t.Helper()
	m, err := DecodeModels(strings.NewReader(mustFixture(t, "testdata/models-min.json")), DefaultBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	h, err := DecodeHistory(strings.NewReader(mustFixture(t, "testdata/history-min.json")), DefaultBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	return FetchResult{Catalogs: CatalogSet{Models: m, History: h}, Validators: Validators{Models: Validator{ETag: `"m"`}, History: Validator{ETag: `"h"`}}}
}
