package lists

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/strace-me/lotsman/pkg/rulesets"
	"github.com/strace-me/lotsman/pkg/subscription"
)

var categoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type SRSProvider struct {
	ProviderName string
	LiveDir      string
	Decompiler   rulesets.EntryDecompiler
}

func (p SRSProvider) Name() string { return p.ProviderName }

func (p SRSProvider) Load(_ context.Context, ref Reference, _ subscription.Fetcher) ([][]byte, error) {
	if p.Decompiler == nil {
		return nil, fmt.Errorf("provider %q has no srs decompiler", p.ProviderName)
	}
	if ref.Category == "" || !categoryPattern.MatchString(ref.Category) {
		return nil, fmt.Errorf("invalid provider category %q", ref.Category)
	}
	var rel string
	switch ref.Dataset {
	case "geosite":
		rel = filepath.Join("rule-set-geosite", "geosite-"+ref.Category+".srs")
	case "geoip":
		rel = filepath.Join("rule-set-geoip", "geoip-"+ref.Category+".srs")
	default:
		return nil, fmt.Errorf("unsupported provider dataset %q", ref.Dataset)
	}
	domains, cidrs, err := p.Decompiler.Entries(filepath.Join(p.LiveDir, rel))
	if err != nil {
		return nil, err
	}
	var values []string
	switch ref.Kind {
	case Domains:
		values = domains
	case IPs:
		values = cidrs
	default:
		return nil, fmt.Errorf("unsupported list kind %q", ref.Kind)
	}
	return [][]byte{[]byte(strings.Join(values, "\n"))}, nil
}
