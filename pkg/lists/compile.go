package lists

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type Representation struct {
	Engine   string
	Kind     Kind
	Filename string
	Content  string
}

type Compiler interface {
	Engine() string
	Compile(Snapshot) (Representation, error)
}

type Compilers []Compiler

func (c Compilers) CompileAll(snapshot Snapshot) ([]Representation, error) {
	out := make([]Representation, 0, len(c))
	for _, compiler := range c {
		representation, err := compiler.Compile(snapshot)
		if err != nil {
			return nil, fmt.Errorf("engine %q: %w", compiler.Engine(), err)
		}
		out = append(out, representation)
	}
	return out, nil
}

// HostlistCompiler renders a plain newline-separated hostlist for nfqws engines.
type HostlistCompiler struct {
	EngineName string
}

func (c HostlistCompiler) Engine() string { return c.EngineName }

func (c HostlistCompiler) Compile(snapshot Snapshot) (Representation, error) {
	if snapshot.Kind != Domains {
		return Representation{}, fmt.Errorf("engine %q hostlist cannot represent %s list %q", c.EngineName, snapshot.Kind, snapshot.Name)
	}
	return Representation{
		Engine:   c.EngineName,
		Kind:     snapshot.Kind,
		Filename: snapshot.Name + ".hostlist",
		Content:  strings.Join(snapshot.Values, "\n") + "\n",
	}, nil
}

// SingboxRuleSetCompiler renders the sing-box rule-set source JSON, which
// `sing-box rule-set compile` turns into a binary .srs.
type SingboxRuleSetCompiler struct{}

func (SingboxRuleSetCompiler) Engine() string { return "sing-box" }

func (SingboxRuleSetCompiler) Compile(snapshot Snapshot) (Representation, error) {
	rule := map[string][]string{}
	switch snapshot.Kind {
	case Domains:
		rule["domain_suffix"] = snapshot.Values
	case IPs:
		rule["ip_cidr"] = snapshot.Values
	default:
		return Representation{}, fmt.Errorf("unknown list kind %q", snapshot.Kind)
	}
	data, err := json.Marshal(struct {
		Version int                   `json:"version"`
		Rules   []map[string][]string `json:"rules"`
	}{Version: 3, Rules: []map[string][]string{rule}})
	if err != nil {
		return Representation{}, err
	}
	return Representation{
		Engine:   "sing-box",
		Kind:     snapshot.Kind,
		Filename: filepath.Join("rule-set", snapshot.Name+".json"),
		Content:  string(data) + "\n",
	}, nil
}
