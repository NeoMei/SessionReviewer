package source_test

import (
	"context"
	"errors"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/source"
)

type contractAdapter struct{}

func (contractAdapter) Discover(context.Context) (source.Discovery, error) {
	return source.Discovery{}, nil
}

func (contractAdapter) Freeze(context.Context, source.Candidate) (source.Boundary, error) {
	return source.Boundary{}, nil
}

func (contractAdapter) Decode(context.Context, source.Boundary, func(memory.ObservationRevision) error) (source.DecodeReport, error) {
	return source.DecodeReport{}, nil
}

func (contractAdapter) Read(context.Context, memory.SourceRef, int64) ([]byte, error) {
	return nil, nil
}

var _ source.Adapter = contractAdapter{}

func TestValidateReadLimitEnforcesOneThrough64KiB(t *testing.T) {
	for _, limit := range []int64{1, source.MaxReadBytes} {
		if err := source.ValidateReadLimit(limit); err != nil {
			t.Fatalf("limit %d rejected: %v", limit, err)
		}
	}
	for _, limit := range []int64{0, -1, source.MaxReadBytes + 1} {
		if err := source.ValidateReadLimit(limit); !errors.Is(err, source.ErrReadLimit) {
			t.Fatalf("limit %d error = %v, want ErrReadLimit", limit, err)
		}
	}
}

func TestDecodeReportCarriesLineageWithoutSelectingAnActiveRevision(t *testing.T) {
	key := memory.ObservationKey{
		Provider: "codex", SessionID: "session-a", SourceIdentity: "source-a",
		Sequence: 7, ProjectID: "project-a", Kind: "command", Subject: "call-a",
	}
	report := source.DecodeReport{Supersessions: []source.RevisionSupersession{{
		Key: key, SupersededRevisionID: "sha256:old", SuccessorRevisionID: "sha256:new",
		SupersededAdapter: "v1", SuccessorAdapter: "v2",
	}}}

	got := report.Supersessions[0]
	if got.Key != key || got.SupersededRevisionID != "sha256:old" || got.SuccessorRevisionID != "sha256:new" {
		t.Fatalf("supersession metadata changed: %+v", got)
	}
}
