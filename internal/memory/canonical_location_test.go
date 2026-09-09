package memory

import (
	"encoding/json"
	"testing"
)

func TestCanonicalSourceCoordinatesValidateExactUnion(t *testing.T) {
	for _, tt := range []struct {
		name, wire string
		valid      bool
	}{
		{"canonical", `{"kind":"canonical","canonical":{"record":9}}`, true},
		{"largest canonical", `{"kind":"canonical","canonical":{"record":9007199254740991}}`, true},
		{"zero canonical", `{"kind":"canonical","canonical":{"record":0}}`, false},
		{"negative canonical", `{"kind":"canonical","canonical":{"record":-1}}`, false},
		{"unsafe canonical", `{"kind":"canonical","canonical":{"record":9007199254740992}}`, false},
		{"missing canonical", `{"kind":"canonical"}`, false},
		{"canonical with jsonl", `{"kind":"canonical","canonical":{"record":9},"jsonl":{"line":9,"byte_offset":128}}`, false},
		{"jsonl with canonical", `{"kind":"jsonl","canonical":{"record":9},"jsonl":{"line":9,"byte_offset":128}}`, false},
		{"mismatched discriminator", `{"kind":"jsonl","canonical":{"record":9}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var location SourceLocation
			if err := json.Unmarshal([]byte(tt.wire), &location); err != nil {
				t.Fatal(err)
			}
			revision := validObservation(validObservationKey(), "adapter-1", nil)
			revision.Ref.Location = location
			revision.RevisionID = ObservationRevisionID(revision)
			if err := ValidateObservationRevision(revision); (err == nil) != tt.valid {
				t.Fatalf("observation validation=%v want valid=%v", err, tt.valid)
			}
			record := validSourceRecord()
			record.FrozenBoundary.Location = location
			if err := ValidateSourceRecord(record); (err == nil) != tt.valid {
				t.Fatalf("boundary validation=%v want valid=%v", err, tt.valid)
			}
			if tt.valid {
				body, err := json.Marshal(location)
				if err != nil || string(body) != tt.wire {
					t.Fatalf("coordinate roundtrip=%s err=%v", body, err)
				}
			}
		})
	}
}

func TestJSONLSourceCoordinateWireCompatibility(t *testing.T) {
	body, err := json.Marshal(SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 9, ByteOffset: 128}})
	if err != nil || string(body) != `{"kind":"jsonl","jsonl":{"line":9,"byte_offset":128}}` {
		t.Fatalf("legacy hashed coordinate bytes changed: %s err=%v", body, err)
	}
}

func TestCanonicalSourceLocationSchemaParity(t *testing.T) {
	for _, wire := range []string{
		`{"kind":"canonical","canonical":{"record":9}}`,
		`{"kind":"canonical","canonical":{"record":9007199254740991}}`,
		`{"kind":"canonical","canonical":{"record":0}}`,
		`{"kind":"canonical","canonical":{"record":9007199254740992}}`,
		`{"kind":"canonical","canonical":{"record":9},"jsonl":{"line":9,"byte_offset":128}}`,
		`{"kind":"jsonl","canonical":{"record":9},"jsonl":{"line":9,"byte_offset":128}}`,
		`{"kind":"canonical"}`,
	} {
		var location SourceLocation
		if err := json.Unmarshal([]byte(wire), &location); err != nil {
			t.Fatal(err)
		}
		valid := validateSourceLocation("opencode", location) == nil
		for _, tt := range []struct {
			schema    string
			value     any
			container string
		}{
			{"observation-v1", validObservation(validObservationKey(), "adapter-1", nil), "source_ref"},
			{"source-catalog-v1", validSourceRecord(), "frozen_boundary"},
		} {
			body, _ := json.Marshal(tt.value)
			var object map[string]any
			if err := json.Unmarshal(body, &object); err != nil {
				t.Fatal(err)
			}
			var raw any
			if err := json.Unmarshal([]byte(wire), &raw); err != nil {
				t.Fatal(err)
			}
			object[tt.container].(map[string]any)["source_location"] = raw
			body, _ = json.Marshal(object)
			if err := validateJSONSchemaFixture("../../schemas/"+tt.schema+".schema.json", body); (err == nil) != valid {
				t.Errorf("%s location=%s validation=%v want valid=%v", tt.schema, wire, err, valid)
			}
		}
	}
}

func TestRecordOrdinalRejectsInvalidCoordinates(t *testing.T) {
	for _, tt := range []struct {
		location SourceLocation
		want     int
	}{
		{SourceLocation{Kind: SourceLocationCanonical, Canonical: &CanonicalSourceLocation{Record: 7}}, 7},
		{SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 9, ByteOffset: 128}}, 9},
		{SourceLocation{Kind: SourceLocationCanonical, Canonical: &CanonicalSourceLocation{Record: -1}}, 0},
		{SourceLocation{Kind: SourceLocationCanonical, Canonical: &CanonicalSourceLocation{Record: maxSafeInteger + 1}}, 0},
		{SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 9, ByteOffset: -1}}, 0},
		{SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 9}, Canonical: &CanonicalSourceLocation{Record: 7}}, 0},
		{SourceLocation{Kind: "unknown", Canonical: &CanonicalSourceLocation{Record: 7}}, 0},
		{SourceLocation{}, 0},
	} {
		if got := tt.location.RecordOrdinal(); got != tt.want {
			t.Errorf("location=%+v ordinal=%d want=%d", tt.location, got, tt.want)
		}
	}
}

func TestGenericToolNameFieldMatchesObservationSchema(t *testing.T) {
	value := validObservation(validObservationKey(), "adapter-1", map[string]string{"tool_id": "call_read", "tool_name": "read", "status": "completed"})
	value.Key.Kind = "tool"
	value.Operation = "tool_result"
	value.RevisionID = ObservationRevisionID(value)
	if err := ValidateObservationRevision(value); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/observation-v1.schema.json", body); err != nil {
		t.Fatal(err)
	}
	view := validSessionView()
	view.ObservationSummaries[0].Fields = map[string]string{"tool_id": "call_read", "tool_name": "read", "status": "completed"}
	body, err = json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/session-view-v1.schema.json", body); err != nil {
		t.Fatal(err)
	}
}
