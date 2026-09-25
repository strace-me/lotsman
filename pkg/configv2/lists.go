package configv2

import "github.com/strace-me/lotsman/pkg/lists"

// Definitions maps the v2 list declarations onto the loader's definitions.
func (l Lists) Definitions() (domains, ips map[string]lists.Definition) {
	domains = make(map[string]lists.Definition, len(l.Domains))
	for name, list := range l.Domains {
		domains[name] = definition(name, list, lists.Domains)
	}
	ips = make(map[string]lists.Definition, len(l.IPs))
	for name, list := range l.IPs {
		ips[name] = definition(name, list, lists.IPs)
	}
	return domains, ips
}

func definition(name string, list List, kind lists.Kind) lists.Definition {
	return lists.Definition{
		Name:     name,
		Kind:     kind,
		Files:    list.Files,
		URLs:     list.URLs,
		Domains:  list.Domains,
		CIDRs:    list.CIDRs,
		Source:   list.Source,
		Dataset:  list.Dataset,
		Category: list.Category,
	}
}
