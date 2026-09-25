package lists

import (
	"context"
	"fmt"

	"github.com/strace-me/lotsman/pkg/subscription"
)

type Reference struct {
	Kind     Kind
	Dataset  string
	Category string
}

type Provider interface {
	Name() string
	Load(context.Context, Reference, subscription.Fetcher) ([][]byte, error)
}

type Providers map[string]Provider

func (p Providers) Load(ctx context.Context, name string, ref Reference, fetcher subscription.Fetcher) ([][]byte, error) {
	provider, ok := p[name]
	if !ok {
		return nil, fmt.Errorf("list provider %q is not registered", name)
	}
	return provider.Load(ctx, ref, fetcher)
}
