package lists

import (
	"context"
	"fmt"
	"time"

	"github.com/strace-me/lotsman/pkg/subscription"
)

type Reference struct {
	Kind     Kind
	Dataset  string
	Category string
}

type Payload struct {
	Data      [][]byte
	UpdatedAt time.Time
}

type Provider interface {
	Name() string
	Load(context.Context, Reference, subscription.Fetcher) (Payload, error)
}

type Providers map[string]Provider

func (p Providers) Load(ctx context.Context, name string, ref Reference, fetcher subscription.Fetcher) (Payload, error) {
	provider, ok := p[name]
	if !ok {
		return Payload{}, fmt.Errorf("list provider %q is not registered", name)
	}
	return provider.Load(ctx, ref, fetcher)
}
