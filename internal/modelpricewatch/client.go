package modelpricewatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const (
	ModelsURL  = "https://modelpricewatch.com/api/v1/models.json"
	HistoryURL = "https://modelpricewatch.com/api/v1/price-history.json"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type Client struct{ doer HTTPDoer }
type Validator struct {
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
}
type Validators struct {
	Models  Validator `json:"models"`
	History Validator `json:"history"`
}
type FetchResult struct {
	Catalogs    CatalogSet
	Validators  Validators
	NotModified bool
}

func NewClient(doer HTTPDoer) *Client {
	if doer == nil {
		doer = http.DefaultClient
	}
	if httpClient, ok := doer.(*http.Client); ok {
		copyClient := *httpClient
		priorCheck := copyClient.CheckRedirect
		copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, "modelpricewatch.com") || req.URL.User != nil {
				return errors.New("unsafe redirect outside fixed HTTPS origin")
			}
			if priorCheck != nil {
				return priorCheck(req, via)
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		}
		doer = &copyClient
	}
	return &Client{doer: doer}
}

func (c *Client) Fetch(ctx context.Context, validators Validators) (FetchResult, error) {
	if c == nil || c.doer == nil {
		return FetchResult{}, errors.New("modelpricewatch HTTP client is required")
	}
	modelsBody, mv, models304, err := c.fetchOne(ctx, ModelsURL, validators.Models)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch models: %w", err)
	}
	historyBody, hv, history304, err := c.fetchOne(ctx, HistoryURL, validators.History)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch history: %w", err)
	}
	if models304 != history304 {
		return FetchResult{}, errors.New("partial catalog not-modified response")
	}
	if models304 {
		return FetchResult{Validators: validators, NotModified: true}, nil
	}
	models, err := DecodeModels(strings.NewReader(string(modelsBody)), DefaultBodyLimit)
	if err != nil {
		return FetchResult{}, fmt.Errorf("decode models: %w", err)
	}
	history, err := DecodeHistory(strings.NewReader(string(historyBody)), DefaultBodyLimit)
	if err != nil {
		return FetchResult{}, fmt.Errorf("decode history: %w", err)
	}
	set := CatalogSet{Models: models, History: history}
	if err := validatePair(set); err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Catalogs: set, Validators: Validators{Models: mv, History: hv}}, nil
}

func (c *Client) fetchOne(ctx context.Context, endpoint string, validator Validator) ([]byte, Validator, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, Validator{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	if validator.ETag != "" {
		req.Header.Set("If-None-Match", validator.ETag)
	}
	if validator.LastModified != "" {
		req.Header.Set("If-Modified-Since", validator.LastModified)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, Validator{}, false, err
	}
	if resp == nil || resp.Body == nil {
		return nil, Validator{}, false, errors.New("empty HTTP response")
	}
	defer resp.Body.Close()
	if err := validateResponseOrigin(resp, endpoint); err != nil {
		return nil, Validator{}, false, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return nil, validator, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, Validator{}, false, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, Validator{}, false, errors.New("response content type is not application/json")
	}
	body, err := readCompleteBounded(resp.Body, DefaultBodyLimit)
	if err != nil {
		return nil, Validator{}, false, err
	}
	return body, Validator{ETag: boundedHeader(headerValue(resp.Header, "ETag")), LastModified: boundedHeader(headerValue(resp.Header, "Last-Modified"))}, false, nil
}

func validateResponseOrigin(resp *http.Response, endpoint string) error {
	if resp.Request == nil || resp.Request.URL == nil {
		return errors.New("response URL is unavailable")
	}
	want, _ := url.Parse(endpoint)
	got := resp.Request.URL
	if got.Scheme != "https" || !strings.EqualFold(got.Host, want.Host) || got.User != nil {
		return errors.New("unsafe redirect outside fixed HTTPS origin")
	}
	return nil
}
func boundedHeader(v string) string {
	if len(v) > 1024 {
		return ""
	}
	return v
}

func headerValue(header http.Header, name string) string {
	for key, values := range header {
		if strings.EqualFold(key, name) && len(values) != 0 {
			return values[0]
		}
	}
	return ""
}

var _ io.Reader = (*strings.Reader)(nil)
