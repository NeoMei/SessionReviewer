package opencode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

func TestUsageRequiresCompleteNativeTokenComponents(t *testing.T) {
	for _, tt := range []struct {
		name, tokens string
		valid        bool
	}{
		{"empty", `{}`, false},
		{"null input", `{"input":null,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}`, false},
		{"missing output", `{"input":10,"reasoning":2,"cache":{"read":3,"write":1}}`, false},
		{"missing reasoning", `{"input":10,"output":5,"cache":{"read":3,"write":1}}`, false},
		{"null cache", `{"input":10,"output":5,"reasoning":2,"cache":null}`, false},
		{"missing cache write", `{"input":10,"output":5,"reasoning":2,"cache":{"read":3}}`, false},
		{"null total", `{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1},"total":null}`, false},
		{"inconsistent total", `{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1},"total":20}`, false},
		{"valid total", `{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1},"total":21}`, true},
		{"explicit zero", `{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0},"total":0}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q, a := messageFixture("msg_1", "user", "question", ""), messageFixture("msg_2", "assistant", "answer", "stop")
			var obj map[string]any
			if err := json.Unmarshal(a.Data, &obj); err != nil {
				t.Fatal(err)
			}
			obj["tokens"] = json.RawMessage(tt.tokens)
			a.Data, _ = json.Marshal(obj)
			records, _, err := stableRecords(sessionRow{ID: "ses_usage", Directory: "/project", Created: 1789000000000}, []rawMessage{q, a})
			if err != nil {
				t.Fatal(err)
			}
			usage, err := recordUsage(records)
			if (err == nil) != tt.valid {
				t.Fatalf("usage=%+v err=%v want valid=%v", usage, err, tt.valid)
			}
		})
	}
}

func TestDecodeMarksIncompleteUsageUnknownAndUsesSessionStart(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		name := "complete"
		if incomplete {
			name = "incomplete"
		}
		t.Run(name, func(t *testing.T) {
			db, path, binding, catalog := adapterFixture(t)
			answer := messageFixture("msg_2", "assistant", "Answer", "stop")
			if incomplete {
				var obj map[string]any
				json.Unmarshal(answer.Data, &obj)
				obj["tokens"] = map[string]any{}
				answer.Data, _ = json.Marshal(obj)
			}
			insertSession(t, db, "ses_usage", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), answer})
			if _, err := db.Exec(`UPDATE session SET time_created=1788999990000 WHERE id='ses_usage'`); err != nil {
				t.Fatal(err)
			}
			r := redact.Default()
			a, err := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
			if err != nil {
				t.Fatal(err)
			}
			d, err := a.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			b, err := a.Freeze(context.Background(), d.Candidates[0])
			if err != nil {
				t.Fatal(err)
			}
			report, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for _, d := range report.Diagnostics {
				if d.Code == "usage_unavailable" {
					unknown = true
				}
			}
			if unknown != incomplete {
				t.Fatalf("usage completeness not reported: %+v", report)
			}
			if report.ProposedSource.Usage.StartedAt != "2026-09-10T00:26:30Z" || report.ProposedSource.Usage.DurationMS != 11000 {
				t.Fatalf("usage start/duration excluded idle session time: %+v", report.ProposedSource.Usage)
			}
		})
	}
}

func TestDiscoveryRejectsAcceptedPrefixReducedToZeroStableRecords(t *testing.T) {
	for _, mutation := range []string{`DELETE FROM part WHERE message_id='ses_drift_msg_2'; DELETE FROM message WHERE id='ses_drift_msg_2'`, `UPDATE message SET data=json_set(data,'$.finish','tool-calls') WHERE id='ses_drift_msg_2'`} {
		t.Run(mutation, func(t *testing.T) {
			db, path, binding, catalog := adapterFixture(t)
			insertSession(t, db, "ses_drift", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
			r := redact.Default()
			a, _ := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
			d, err := a.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			b, err := a.Freeze(context.Background(), d.Candidates[0])
			if err != nil {
				t.Fatal(err)
			}
			report, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.UpsertSource(report.ProposedSource); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Discover(context.Background()); err == nil {
				t.Fatal("accepted prefix deletion was downgraded to no_finalized_records")
			}
		})
	}
}

func TestRepeatedDiscoveryKeepsIndependentCandidateLeases(t *testing.T) {
	db, path, binding, catalog := adapterFixture(t)
	insertSession(t, db, "ses_leases", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	r := redact.Default()
	api, _ := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	a := api.(*adapter)
	first, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidates[0].Handle != second.Candidates[0].Handle || first.Candidates[0].Lease == second.Candidates[0].Lease {
		t.Fatal("stable boundary lost independent occurrence identity")
	}
	boundaries := []source.Boundary{}
	for _, c := range []source.Candidate{first.Candidates[0], second.Candidates[0]} {
		b, err := a.Freeze(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, b)
	}
	for _, b := range boundaries {
		if _, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range boundaries {
		if _, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil }); err == nil {
			t.Fatal("consumed decode lease reused")
		}
	}
	if len(a.snapshots) != 1 || len(a.readable) != 1 {
		t.Fatalf("equivalent decoded snapshots were not deduplicated: snapshots=%d readable=%d", len(a.snapshots), len(a.readable))
	}
	for _, snapshot := range a.snapshots {
		if snapshot.references != 1 || snapshot.bytes != a.retainedBytes {
			t.Fatalf("decoded cache retains consumed lease references: references=%d bytes=%d retained=%d", snapshot.references, snapshot.bytes, a.retainedBytes)
		}
	}
}

func TestCandidateBaselineIsDefensiveAndAbandonReleasesRawSnapshots(t *testing.T) {
	db, path, binding, catalog := adapterFixture(t)
	insertSession(t, db, "ses_clone", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	r := redact.Default()
	api, _ := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	a := api.(*adapter)
	first, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Freeze(context.Background(), first.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	report, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertSource(report.ProposedSource); err != nil {
		t.Fatal(err)
	}
	next, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := next.Candidates[0]
	original.CatalogBaseline = cloneBaseline(original.CatalogBaseline)
	next.Candidates[0].CatalogBaseline.PriorSource.FrozenBoundary.SourceHash = strings.Repeat("f", 64)
	if _, err := a.Freeze(context.Background(), next.Candidates[0]); err == nil {
		t.Fatal("caller mutation altered owned baseline authority")
	}
	owned, err := a.Freeze(context.Background(), original)
	if err != nil {
		t.Fatalf("defensive original invalidated: %v", err)
	}
	a.AbandonBoundary(owned)
	extra, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.AbandonCandidate(extra.Candidates[0])
	if len(a.candidates) != 0 || len(a.boundaries) != 0 {
		t.Fatalf("abandoned/consumed occurrences retain raw snapshots: candidates=%d boundaries=%d", len(a.candidates), len(a.boundaries))
	}
}

func TestFinalNonBashAndMalformedBashToolsRemainTypedFacts(t *testing.T) {
	db, path, binding, catalog := adapterFixture(t)
	answer := messageFixture("msg_2", "assistant", "Answer", "stop")
	answer.Parts = append(answer.Parts,
		rawPart{ID: "prt_read", Data: json.RawMessage(`{"type":"tool","callID":"call_read","tool":"read","state":{"status":"completed","input":{"filePath":"private.txt"},"output":"do not retain"}}`)},
		rawPart{ID: "prt_search", Data: json.RawMessage(`{"type":"tool","callID":"call_search","tool":"web_search","state":{"status":"error","error":"do not retain"}}`)},
		rawPart{ID: "prt_bash", Data: json.RawMessage(`{"type":"tool","callID":"call_bash","tool":"bash","state":{"status":"completed","input":{"command":42},"metadata":{"exit":0}}}`)})
	insertSession(t, db, "ses_tools", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), answer})
	r := redact.Default()
	a, _ := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	d, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Freeze(context.Background(), d.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	calls, results := map[string]string{}, map[string]string{}
	report, err := a.Decode(context.Background(), b, func(v memory.ObservationRevision) error {
		if v.Key.Kind == "command" || v.Key.Kind == "verification" {
			t.Fatal("unstructured tool promoted to shell or verification")
		}
		if v.Operation == "tool_call" {
			calls[v.Fields["tool_id"]] = v.Fields["tool_name"]
		}
		if v.Operation == "tool_result" {
			results[v.Fields["tool_id"]] = v.Fields["status"]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || len(results) != 3 || calls["call_read"] != "read" || calls["call_search"] != "web_search" || calls["call_bash"] != "bash" || results["call_read"] != "completed" || results["call_search"] != "error" {
		t.Fatalf("typed tool facts missing: calls=%v results=%v", calls, results)
	}
	diagnostic := false
	for _, d := range report.Diagnostics {
		if d.Code == "tool_input_unsupported" {
			diagnostic = true
		}
	}
	if !diagnostic {
		t.Fatalf("malformed bash input disappeared without diagnostic: %+v", report.Diagnostics)
	}
}

func TestConsecutiveAssistantRecordsKeepOwnerAndDeferPendingOwner(t *testing.T) {
	q, a := messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")
	follow := messageFixture("msg_3", "assistant", "Follow-up", "stop")
	row := sessionRow{ID: "ses_owner", Directory: "/project", Created: 1789000000000}
	records, deferred, err := stableRecords(row, []rawMessage{q, a, follow})
	if err != nil || deferred || len(records) != 3 {
		t.Fatalf("consecutive assistant lost owner: records=%d deferred=%v err=%v", len(records), deferred, err)
	}
	follow.Parts = append(follow.Parts, rawPart{ID: "prt_running", Data: json.RawMessage(`{"type":"tool","callID":"call_run","tool":"bash","state":{"status":"running","input":{"command":"go test ./..."}}}`)})
	records, deferred, err = stableRecords(row, []rawMessage{q, a, follow})
	if err != nil || !deferred || len(records) != 0 {
		t.Fatalf("pending same-owner suffix left partial accepted turn: records=%d deferred=%v err=%v", len(records), deferred, err)
	}
}

func TestAbandonedAndFailedDecodeReleaseSnapshotBudget(t *testing.T) {
	db, path, binding, catalog := adapterFixture(t)
	insertSession(t, db, "ses_cleanup", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	r := redact.Default()
	api, err := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	if err != nil {
		t.Fatal(err)
	}
	a := api.(*adapter)
	for _, stage := range []string{"candidate", "boundary", "decode failure"} {
		t.Run(stage, func(t *testing.T) {
			d, err := a.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if a.retainedBytes == 0 {
				t.Fatal("discovered snapshot not admitted to byte budget")
			}
			if stage == "candidate" {
				a.AbandonCandidate(d.Candidates[0])
			} else {
				b, err := a.Freeze(context.Background(), d.Candidates[0])
				if err != nil {
					t.Fatal(err)
				}
				if stage == "boundary" {
					a.AbandonBoundary(b)
				} else {
					_, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return context.Canceled })
					if err != context.Canceled {
						t.Fatalf("visitor failure=%v", err)
					}
				}
			}
			if a.retainedBytes != 0 || len(a.snapshots) != 0 || len(a.candidates) != 0 || len(a.boundaries) != 0 || len(a.candidateLeases) != 0 || len(a.boundaryLeases) != 0 {
				t.Fatalf("released occurrence retained raw budget: bytes=%d snapshots=%d", a.retainedBytes, len(a.snapshots))
			}
		})
	}
}

func TestDecodeVisitorCanAuthenticateCurrentFrozenRecord(t *testing.T) {
	db, path, binding, catalog := adapterFixture(t)
	insertSession(t, db, "ses_read", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	r := redact.Default()
	a, err := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Freeze(context.Background(), d.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Decode(context.Background(), b, func(v memory.ObservationRevision) error {
		body, err := a.Read(context.Background(), v.Ref, 4096)
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "Question") {
			t.Fatalf("wrong frozen record read: %s", body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("active frozen record lost bounded read access: %v", err)
	}
}

func TestOptionalSessionTokenTotalsReconcileOnlyFinalizedSnapshots(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		supplied              any
		deferred, wantUnknown bool
	}{
		{"matching", int64(10), false, false},
		{"supplied zero mismatch", int64(0), false, true},
		{"malformed", "unknown", false, true},
		{"null not supplied", nil, false, false},
		{"active totals excluded", int64(0), true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, path, binding, catalog := adapterFixture(t)
			messages := []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")}
			if tt.deferred {
				messages = append(messages, messageFixture("msg_3", "user", "Still running", ""))
			}
			insertSession(t, db, "ses_nativeTotals", binding.CanonicalRoot, messages)
			r := redact.Default()
			a, err := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
			if err != nil {
				t.Fatal(err)
			}
			decode := func() source.DecodeReport {
				t.Helper()
				d, err := a.Discover(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				b, err := a.Freeze(context.Background(), d.Candidates[0])
				if err != nil {
					t.Fatal(err)
				}
				report, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				return report
			}
			before := decode()
			if _, err := db.Exec(`ALTER TABLE session ADD COLUMN tokens_input INTEGER; ALTER TABLE session ADD COLUMN tokens_output INTEGER; ALTER TABLE session ADD COLUMN tokens_reasoning INTEGER; ALTER TABLE session ADD COLUMN tokens_cache_read INTEGER; ALTER TABLE session ADD COLUMN tokens_cache_write INTEGER;`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE session SET tokens_input=?,tokens_output=5,tokens_reasoning=2,tokens_cache_read=3,tokens_cache_write=1 WHERE id='ses_nativeTotals'`, tt.supplied); err != nil {
				t.Fatal(err)
			}
			after := decode()
			unknown := false
			for _, d := range after.Diagnostics {
				if d.Code == "usage_unavailable" {
					unknown = true
				}
			}
			if unknown != tt.wantUnknown {
				t.Fatalf("Session totals reconciliation missing: unknown=%v want=%v usage=%+v", unknown, tt.wantUnknown, after.ProposedSource.Usage)
			}
			if before.ProposedSource.FrozenBoundary.SourceHash != after.ProposedSource.FrozenBoundary.SourceHash {
				t.Fatal("mutable session totals changed canonical source prefix hash")
			}
			if tt.wantUnknown && len(after.ProposedSource.Usage.Models) != 0 {
				t.Fatal("disputed exact usage breakdown retained")
			}
		})
	}
}

func TestSessionTotalsRefreshAcceptedCatalogBothDirections(t *testing.T) {
	db, path, binding, catalog := adapterFixture(t)
	insertSession(t, db, "ses_usageRefresh", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	r := redact.Default()
	a, err := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	if err != nil {
		t.Fatal(err)
	}
	decode := func() source.DecodeReport {
		t.Helper()
		d, err := a.Discover(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		b, err := a.Freeze(context.Background(), d.Candidates[0])
		if err != nil {
			t.Fatal(err)
		}
		report, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	original := decode()
	if _, err := catalog.UpsertSource(original.ProposedSource); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE session ADD COLUMN tokens_input INTEGER`); err != nil {
		t.Fatal(err)
	}
	for _, supplied := range []int{0, 10} {
		if _, err := db.Exec(`UPDATE session SET tokens_input=?`, supplied); err != nil {
			t.Fatal(err)
		}
		refreshed := decode()
		if refreshed.BoundaryRelation != source.BoundaryRelation("usage_refresh") {
			t.Fatalf("same-prefix accounting change has wrong relation: %q", refreshed.BoundaryRelation)
		}
		if _, err := catalog.ApplyBatch([]sourcecatalog.BatchMutation{{Relation: refreshed.BoundaryRelation, ExpectedDigest: refreshed.ExpectedCatalogDigest, Desired: refreshed.ProposedSource}}); err != nil {
			t.Fatalf("accepted accounting refresh failed: %v", err)
		}
		if refreshed.ProposedSource.FrozenBoundary.SourceHash != original.ProposedSource.FrozenBoundary.SourceHash || refreshed.ProposedSource.SourceIdentity != original.ProposedSource.SourceIdentity {
			t.Fatal("usage refresh changed source authority")
		}
	}
}
