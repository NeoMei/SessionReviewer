package diagram

import "github.com/neomei/SessionReviewer/internal/ledger"

// Render derives project-evolution.md from accepted ledger state. Mermaid is
// output-only: callers must never parse it back into ledger facts.
func Render(state ledger.State) ([]byte, error) {
	return ledger.RenderDiagram(state)
}
