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
// scoped to the providerID passed in, which internal/httpapi sets from the
// caller's authenticated providerId claim (Fase 9), never a client-supplied
// path/body value — see internal/httpapi/wagering.go's
// getWagerTransactionByProvider.
func (g *WagerTransactionGetter) GetByProvider(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error) {
	return g.wagers.FindByProviderAndExternalID(ctx, providerID, externalTransactionID)
}
