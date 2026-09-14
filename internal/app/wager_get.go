package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/wager"
)

type WagerTransactionGetter struct {
	wagers WagerRepository
}

func NewWagerTransactionGetter(wagers WagerRepository) *WagerTransactionGetter {
	return &WagerTransactionGetter{wagers: wagers}
}

// Get looks a transaction up by its internal id — this is not a
// provider-facing route (providers never see internal ids, only their own
// externalTransactionId), so no provider isolation applies here.
func (g *WagerTransactionGetter) Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	return g.wagers.FindByID(ctx, id)
}

// GetByProvider looks a transaction up by (providerId, externalTransactionId)
// — the provider-facing route. Isolation is structural: the lookup is always
// scoped to the providerID passed in, which callers (Fase 7/9) must set to
// the authenticated identity's own providerId, never a client-supplied one.
func (g *WagerTransactionGetter) GetByProvider(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error) {
	return g.wagers.FindByProviderAndExternalID(ctx, providerID, externalTransactionID)
}
