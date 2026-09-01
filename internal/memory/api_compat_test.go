package memory

import "context"

// These assignments are source-compatibility probes. A variadic parameter is
// not assignable to the original function type even when ordinary calls still
// compile, so each public function altered by the retention cancellation work
// is pinned explicitly here.
var (
	_ func(any) (string, error)                                = Digest
	_ func(context.Context, any) (string, error)               = DigestContext
	_ func(ObservationRevision) string                         = ObservationRevisionID
	_ func(context.Context, ObservationRevision) string        = ObservationRevisionIDContext
	_ func(SessionView) (string, error)                        = SessionViewDigest
	_ func(context.Context, SessionView) (string, error)       = SessionViewDigestContext
	_ func(ProjectProbeState) (string, error)                  = ProjectProbeStateDigest
	_ func(context.Context, ProjectProbeState) (string, error) = ProjectProbeStateDigestContext
	_ func(ProjectView) (string, error)                        = ProjectViewDigest
	_ func(context.Context, ProjectView) (string, error)       = ProjectViewDigestContext

	_ func(ObservationRevision) error                  = ValidateObservationRevision
	_ func(context.Context, ObservationRevision) error = ValidateObservationRevisionContext
	_ func(SessionView) error                          = ValidateSessionView
	_ func(context.Context, SessionView) error         = ValidateSessionViewContext
	_ func(ProjectProbeState) error                    = ValidateProjectProbeState
	_ func(context.Context, ProjectProbeState) error   = ValidateProjectProbeStateContext
	_ func(ProbeCheck) error                           = ValidateProbeCheck
	_ func(context.Context, ProbeCheck) error          = ValidateProbeCheckContext
	_ func(ProjectView) error                          = ValidateProjectView
	_ func(context.Context, ProjectView) error         = ValidateProjectViewContext
	_ func(GenerationManifest) error                   = ValidateGenerationManifest
	_ func(context.Context, GenerationManifest) error  = ValidateGenerationManifestContext
)
