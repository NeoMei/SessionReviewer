package modelpricewatch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientFetchesOnlyFixedOriginsWithConditionalValidators(t *testing.T) {
	models := mustFixture(t, "testdata/models-min.json")
	history := mustFixture(t, "testdata/history-min.json")
	requests := 0
	doer := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != ModelsURL && req.URL.String() != HistoryURL {
			t.Fatalf("url=%s", req.URL)
		}
		if req.Header.Get("If-None-Match") != `"old"` || req.Header.Get("If-Modified-Since") != "Mon, 01 Sep 2026 00:00:00 GMT" {
			t.Fatalf("headers=%v", req.Header)
		}
		body := models
		etag := `"models"`
		if req.URL.String() == HistoryURL {
			body = history
			etag = `"history"`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "ETag": {etag}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	got, err := NewClient(doer).Fetch(context.Background(), Validators{Models: Validator{ETag: `"old"`, LastModified: "Mon, 01 Sep 2026 00:00:00 GMT"}, History: Validator{ETag: `"old"`, LastModified: "Mon, 01 Sep 2026 00:00:00 GMT"}})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || got.NotModified || got.Catalogs.Models.Count != 1 || got.Validators.History.ETag != `"history"` {
		t.Fatalf("got=%#v requests=%d", got, requests)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientRequiresPaired304AndJSONSuccess(t *testing.T) {
	models := mustFixture(t, "testdata/models-min.json")
	for _, test := range []struct {
		name        string
		status      [2]int
		contentType string
		want        string
	}{
		{"paired not modified", [2]int{304, 304}, "application/json", ""},
		{"partial not modified", [2]int{304, 200}, "application/json", "partial"},
		{"wrong content type", [2]int{200, 200}, "text/plain", "content type"},
		{"failure status", [2]int{429, 429}, "application/json", "status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			doer := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				status := test.status[calls]
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(models)), Request: req}, nil
			})
			got, err := NewClient(doer).Fetch(context.Background(), Validators{})
			if test.want == "" {
				if err != nil || !got.NotModified {
					t.Fatalf("got=%#v err=%v", got, err)
				}
			} else if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestClientRejectsCrossOriginFinalResponse(t *testing.T) {
	doer := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		redirected := req.Clone(req.Context())
		redirected.URL, _ = url.Parse("https://evil.example/catalog.json")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: redirected}, nil
	})
	if _, err := NewClient(doer).Fetch(context.Background(), Validators{}); err == nil || !strings.Contains(err.Error(), "unsafe redirect") {
		t.Fatalf("err=%v", err)
	}
}

func TestClientStopsCrossOriginRedirectBeforeFollowingIt(t *testing.T) {
	followed := false
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "evil.example" {
			followed = true
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/catalog.json"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	_, err := NewClient(&http.Client{Transport: transport}).Fetch(context.Background(), Validators{})
	if err == nil || followed {
		t.Fatalf("err=%v followed=%t", err, followed)
	}
}

func mustFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
