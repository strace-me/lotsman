package zapret

import (
	"fmt"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategystore"
)

type Nfqws2Engine struct{}

func (Nfqws2Engine) Engine() string { return "nfqws2" }

func (Nfqws2Engine) Validate(b strategystore.Bundle) error {
	if b.Manifest.Engine != "nfqws2" {
		return fmt.Errorf("strategy %q is for engine %q, not nfqws2", b.Manifest.ID, b.Manifest.Engine)
	}
	hasAction := false
	for _, arg := range b.Args.Values {
		if strings.HasPrefix(arg, "--lua-desync=") {
			hasAction = true
		}
		for _, prefix := range []string{"--dpi-desync=", "--dpi-desync-fake-", "--dpi-desync-fooling=", "--dpi-desync-split-"} {
			if strings.HasPrefix(arg, prefix) {
				return fmt.Errorf("nfqws2 strategy %q uses nfqws1 argument %q", b.Manifest.ID, arg)
			}
		}
	}
	if !hasAction {
		return fmt.Errorf("nfqws2 strategy %q has no --lua-desync action", b.Manifest.ID)
	}
	return nil
}

func (Nfqws2Engine) Prepare(b strategystore.Bundle, opts strategystore.PrepareOptions) ([]string, error) {
	if err := (Nfqws2Engine{}).Validate(b); err != nil {
		return nil, err
	}
	if opts.QNum <= 0 {
		return nil, fmt.Errorf("nfqws2 strategy %q has no queue number", b.Manifest.ID)
	}
	args := []string{"--qnum=" + fmt.Sprint(opts.QNum)}
	if opts.Fwmark != 0 {
		args = append(args, fmt.Sprintf("--fwmark=0x%x", opts.Fwmark))
	}
	return append(args, b.Args.Values...), nil
}
