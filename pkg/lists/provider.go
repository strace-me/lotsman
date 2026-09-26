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
	// Skipped counts entries the source carried that the requested kind cannot
	// express — regex and keyword rules in a hostlist, say. Non-zero means the
	// list is narrower than the source, which the caller has to be able to see.
	Skipped int
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
