package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/neomei/SessionReviewer/internal/modelpricewatch"
	"github.com/neomei/SessionReviewer/internal/pricing"
)

const pricingHelp = `Resolve audited public API price estimates.

Usage:
  session-reviewer pricing refresh [--data-dir PATH] --json
  session-reviewer pricing catalog list [--data-dir PATH] --json
  session-reviewer pricing supplement --project-id ID --provider ID --session-id ID
    --usage-record-digest DIGEST --expected-ledger-sha256 SHA [--data-dir PATH] --json
  session-reviewer pricing catalog accept --project-id ID --provider ID --session-id ID
    --usage-record-digest DIGEST --expected-ledger-sha256 SHA [--data-dir PATH] --json

Refresh checks the paired public ModelPriceWatch catalogs at most once per 24 hours.
It does not reprice historical usage. Supplement reads pricing-supplement-v1 from stdin.
Catalog accept reads pricing-catalog-selection-v1 from stdin and calculates cost
from a human-confirmed exact billing route and public listing identifier.
`

func runPricing(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runPricingWithCatalogFactory(args, stdin, stdout, stderr, defaultPricingCatalogFactory)
}

func defaultPricingCatalogFactory(dataRoot string) (pricing.CatalogLoader, error) {
	return modelpricewatch.NewCache(filepath.Join(dataRoot, "modelpricewatch"), modelpricewatch.NewClient(&http.Client{Timeout: 20 * time.Second}))
}

func runPricingWithCatalogFactory(args []string, stdin io.Reader, stdout, stderr io.Writer, factory pricingCatalogFactory) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, pricingHelp)
		return 0
	}
	if len(args) > 0 && args[0] == "catalog" {
		request, err := ParsePricingCatalogContract(args)
		if err != nil {
			writeInspectError(stdout, err)
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if request.Command == "catalog-list" {
			response, err := pricingCatalogList(ctx, resolveDataDir(request.DataDir), factory)
			if err != nil {
				writeInspectError(stdout, ContractError{Code: "pricing_catalog_unavailable", Message: "public pricing catalog unavailable; existing prices are unchanged"})
				return 1
			}
			body, err := json.Marshal(response)
			if err != nil || len(body)+1 > MaxInspectResponseBytes {
				writeInspectError(stdout, ContractError{Code: ContractCodeResponseTooLarge, Message: "pricing catalog response exceeds its byte limit"})
				return 1
			}
			body = append(body, '\n')
			if _, err := stdout.Write(body); err != nil {
				fmt.Fprintln(stderr, "pricing output failed")
				return 1
			}
			return 0
		}
		selection, err := readPricingCatalogSelection(stdin)
		if err == nil {
			err = validatePricingCatalogIdentity(request, selection)
		}
		if err != nil {
			writeInspectError(stdout, contractError(err.Error()))
			return 2
		}
		snapshot, err := applyPricingCatalogSelection(ctx, request, selection, factory)
		if err != nil {
			writeInspectError(stdout, err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(snapshot); err != nil {
			fmt.Fprintln(stderr, "pricing output failed")
			return 1
		}
		return 0
	}
	request, err := ParsePricingContract(args)
	if err != nil {
		writeInspectError(stdout, err)
		return 2
	}
	if request.Command == "supplement" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := applyPricingSupplement(ctx, request, stdin)
		if err != nil {
			writeInspectError(stdout, err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(snapshot); err != nil {
			fmt.Fprintln(stderr, "pricing output failed")
			return 1
		}
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cache, err := factory(resolveDataDir(request.DataDir))
	if err != nil {
		writeInspectError(stdout, err)
		return 1
	}
	set, fresh, err := cache.LoadOrRefresh(ctx, time.Now().UTC())
	if err != nil {
		writeInspectError(stdout, ContractError{Code: "pricing_catalog_unavailable", Message: "public pricing catalog unavailable; existing prices are unchanged"})
		return 1
	}
	response := map[string]any{"schema_version": 1, "status": fresh.Status, "retrieved_at": fresh.RetrievedAt.UTC().Format(time.RFC3339), "model_count": set.Models.Count, "history_count": set.History.Count, "refresh_error": fresh.LastRefreshError}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		fmt.Fprintln(stderr, "pricing output failed")
		return 1
	}
	return 0
}
