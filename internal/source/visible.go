package source

import (
	"context"
	"errors"
	"fmt"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
)

var ErrVisibleReaderUnsupported = errors.New("visible source reader capability is unsupported")

// UnsupportedCapabilityError identifies a registered provider whose adapter
// cannot read authenticated visible source prefixes.
type UnsupportedCapabilityError struct {
	Provider string
}

func (err *UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("source provider %q: %v", err.Provider, ErrVisibleReaderUnsupported)
}

func (err *UnsupportedCapabilityError) Unwrap() error { return ErrVisibleReaderUnsupported }

// VisibleReader is an optional provider capability. Implementations must read
// exactly record.FrozenBoundary and return only explicitly visible messages.
type VisibleReader interface {
	ReadVisiblePrefix(context.Context, memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error)
}
