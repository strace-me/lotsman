package lists

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

func (p SRSProvider) Load(_ context.Context, ref Reference, _ subscription.Fetcher) (Payload, error) {
	if p.Decompiler == nil {
		return Payload{}, fmt.Errorf("provider %q has no srs decompiler", p.ProviderName)
	}
	if ref.Category == "" || !categoryPattern.MatchString(ref.Category) {
		return Payload{}, fmt.Errorf("invalid provider category %q", ref.Category)
	}
	var rel string
	switch ref.Dataset {
	case "geosite":
		rel = filepath.Join("rule-set-geosite", "geosite-"+ref.Category+".srs")
	case "geoip":
		rel = filepath.Join("rule-set-geoip", "geoip-"+ref.Category+".srs")
	default:
		return Payload{}, fmt.Errorf("unsupported provider dataset %q", ref.Dataset)
	}
	path := filepath.Join(p.LiveDir, rel)
	domains, cidrs, err := p.Decompiler.Entries(path)
	if err != nil {
		return Payload{}, err
	}
	var values []string
	switch ref.Kind {
	case Domains:
		values = domains
	case IPs:
		values = cidrs
	default:
		return Payload{}, fmt.Errorf("unsupported list kind %q", ref.Kind)
	}
	updated := time.Time{}
	if info, err := os.Stat(path); err == nil {
		updated = info.ModTime()
	}
	return Payload{Data: [][]byte{[]byte(strings.Join(values, "\n"))}, UpdatedAt: updated}, nil
}
