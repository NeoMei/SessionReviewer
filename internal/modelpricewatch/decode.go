package modelpricewatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCollection = 100000

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type modelsWire struct {
	Count   int       `json:"count" required:"true"`
	Updated string    `json:"updated" required:"true"`
	Data    []Listing `json:"data" required:"true"`
}
type historyWire struct {
	Count   int                     `json:"count" required:"true"`
	Updated string                  `json:"updated" required:"true"`
	Data    map[string]ModelHistory `json:"data" required:"true"`
}

func DecodeModels(reader io.Reader, limit int64) (Catalog, error) {
	body, err := readCompleteBounded(reader, limit)
	if err != nil {
		return Catalog{}, err
	}
	var wire modelsWire
	if err := decodeStrict(body, &wire); err != nil {
		return Catalog{}, err
	}
	if wire.Count < 0 || wire.Count > maxCollection || wire.Count != len(wire.Data) {
		return Catalog{}, fmt.Errorf("models count does not match data")
	}
	if err := validDate("updated", wire.Updated); err != nil {
		return Catalog{}, err
	}
	result := Catalog{Count: wire.Count, Updated: wire.Updated, Listings: make(map[string]Listing, len(wire.Data))}
	for index, row := range wire.Data {
		if err := validateListing(&row); err != nil {
			return Catalog{}, fmt.Errorf("listing %d: %w", index, err)
		}
		if _, exists := result.Listings[row.ID]; exists {
			return Catalog{}, fmt.Errorf("duplicate listing id %q", row.ID)
		}
		result.Listings[row.ID] = row
	}
	return result, nil
}

func DecodeHistory(reader io.Reader, limit int64) (HistoryCatalog, error) {
	body, err := readCompleteBounded(reader, limit)
	if err != nil {
		return HistoryCatalog{}, err
	}
	var wire historyWire
	if err := decodeStrict(body, &wire); err != nil {
		return HistoryCatalog{}, err
	}
	if wire.Count < 0 || wire.Count > maxCollection || wire.Count != len(wire.Data) {
		return HistoryCatalog{}, errors.New("history count does not match data")
	}
	if err := validDate("updated", wire.Updated); err != nil {
		return HistoryCatalog{}, err
	}
	for id, model := range wire.Data {
		if !validText(id, 256, true) || !validText(model.Model, 512, true) || !validText(model.Provider, 256, true) || len(model.History) > maxCollection {
			return HistoryCatalog{}, fmt.Errorf("invalid history model %q", id)
		}
		for index := range model.History {
			if err := validateHistory(&model.History[index]); err != nil {
				return HistoryCatalog{}, fmt.Errorf("history %q entry %d: %w", id, index, err)
			}
		}
	}
	return HistoryCatalog{Count: wire.Count, Updated: wire.Updated, Models: wire.Data}, nil
}

func readCompleteBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil || limit <= 0 {
		return nil, errors.New("positive decode limit and reader required")
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("catalog exceeds %d byte limit", limit)
	}
	if !utf8.Valid(body) {
		return nil, errors.New("catalog is not valid UTF-8")
	}
	return body, nil
}

func decodeStrict(body []byte, dst any) error {
	// This adapter has a 128 MiB boundary, twice the private contract codec's
	// limit, so it owns duplicate-key and exact-field decoding at this boundary.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := scanJSON(dec); err != nil {
		return fmt.Errorf("invalid catalog JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode catalog shape: %w", err)
	}
	var raw any
	rawDecoder := json.NewDecoder(bytes.NewReader(body))
	rawDecoder.UseNumber()
	if err := rawDecoder.Decode(&raw); err != nil {
		return err
	}
	return requireJSONFields(raw, reflect.TypeOf(dst).Elem(), "$")
}

func requireJSONFields(raw any, typ reflect.Type, path string) error {
	for typ.Kind() == reflect.Pointer {
		if raw == nil {
			return nil
		}
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be object", path)
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			value, exists := object[name]
			if field.Tag.Get("required") == "true" && !exists {
				return fmt.Errorf("%s.%s is required", path, name)
			}
			if exists && value != nil {
				if err := requireJSONFields(value, field.Type, path+"."+name); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		values, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s must be array", path)
		}
		for i, value := range values {
			if value != nil {
				if err := requireJSONFields(value, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		values, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be object", path)
		}
		for key, value := range values {
			if value != nil {
				if err := requireJSONFields(value, typ.Elem(), path+"."+key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func scanJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("object key is not string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate object key %q", name)
			}
			seen[name] = true
			if err := scanJSON(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := scanJSON(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return errors.New("unexpected delimiter")
}

func validateListing(row *Listing) error {
	if !validText(row.ID, 256, true) || !validText(row.Provider, 256, true) || !validText(row.Model, 512, true) || !validText(row.Category, 128, true) || !validText(row.Status, 128, true) || !validText(row.Released, 64, true) || (row.Parameters != nil && !validText(*row.Parameters, 4096, false)) || len(row.Modality) > 128 || len(row.Tags) > 128 {
		return errors.New("invalid required listing field")
	}
	for _, v := range []*float64{row.InputPerMTok, row.OutputPerMTok, row.CachedInputPerMTok, &row.BlendedCostPerMTok} {
		if !validNumber(v) {
			return errors.New("price must be finite and nonnegative")
		}
	}
	if row.ContextWindow != nil && (*row.ContextWindow < 0 || *row.ContextWindow > 1<<53-1) {
		return errors.New("invalid context window")
	}
	for _, v := range append(append([]string{}, row.Modality...), row.Tags...) {
		if !validText(v, 256, true) {
			return errors.New("invalid listing array value")
		}
	}
	if err := validURL("pricing URL", row.PricingURL); err != nil {
		return err
	}
	if err := validURL("detail URL", row.DetailURL); err != nil {
		return err
	}
	if err := validDate("last_updated", row.LastUpdated); err != nil {
		return err
	}
	if row.PromoUntil != nil {
		if err := validDate("promo_until", *row.PromoUntil); err != nil {
			return err
		}
	}
	if row.PriceNote != nil {
		if !validText(*row.PriceNote, 4096, false) {
			return errors.New("invalid price note")
		}
		row.HasUnstructuredConditions = strings.TrimSpace(*row.PriceNote) != ""
	}
	if row.Evidence != nil {
		if err := validateEvidence(row.Evidence); err != nil {
			return err
		}
	}
	return nil
}
func validateEvidence(v *Evidence) error {
	if err := validDate("evidence as_of", v.AsOf); err != nil {
		return err
	}
	for _, s := range []string{v.Basis, v.Source, v.Confidence, v.CapturedAt, v.Snapshot, v.SHA, v.AnchorSKU, v.AnchorScope} {
		if !validText(s, 4096, true) {
			return errors.New("invalid evidence field")
		}
	}
	if v.Event != nil && !validText(*v.Event, 256, false) {
		return errors.New("invalid evidence event")
	}
	if v.EvidenceURL != nil {
		return validURL("evidence URL", *v.EvidenceURL)
	}
	return nil
}
func validateHistory(v *HistoryEntry) error {
	if err := validDate("history date", v.Date); err != nil {
		return err
	}
	allowed := map[string]bool{"baseline": true, "snapshot": true, "backfill": true, "correction": true, "price_increase": true, "price_change": true, "price_drop": true, "reverify": true, "new_model": true}
	if !allowed[v.Event] {
		return errors.New("unsupported history event")
	}
	for _, p := range []*float64{v.InputPerMTok, v.OutputPerMTok, v.CachedInputPerMTok} {
		if !validNumber(p) {
			return errors.New("history price must be finite and nonnegative")
		}
	}
	if v.ContextWindow != nil && (*v.ContextWindow < 0 || *v.ContextWindow > 1<<53-1) {
		return errors.New("invalid history context window")
	}
	if len(v.Changes) > 128 {
		return errors.New("too many history changes")
	}
	for _, c := range v.Changes {
		if !validText(c.Field, 256, true) || !validText(c.Direction, 64, true) || !validNumber(c.Old) || !validNumber(&c.New) || (c.ChangePct != nil && (math.IsNaN(*c.ChangePct) || math.IsInf(*c.ChangePct, 0))) {
			return errors.New("invalid history change")
		}
	}
	for _, p := range []*string{v.Source, v.Confidence, v.CapturedAt, v.SHA, v.Snapshot, v.SupersedesFrom, v.SupersedesTo, v.Note} {
		if p != nil && !validText(*p, 4096, false) {
			return errors.New("invalid history metadata")
		}
	}
	if v.EvidenceURL != nil {
		return validURL("history evidence URL", *v.EvidenceURL)
	}
	return nil
}
func validNumber(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0)
}
func validText[T ~string](v T, max int, required bool) bool {
	return len(v) <= max && utf8.ValidString(string(v)) && (!required || strings.TrimSpace(string(v)) != "")
}
func validDate(name, value string) error {
	if !dateRE.MatchString(value) {
		return fmt.Errorf("%s must be YYYY-MM-DD", name)
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}
func validURL(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(value) > 2048 {
		return fmt.Errorf("invalid HTTPS %s", name)
	}
	return nil
}
