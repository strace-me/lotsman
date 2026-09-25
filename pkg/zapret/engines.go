package zapret

import "github.com/strace-me/lotsman/pkg/strategystore"

func StrategyEngineAdapters() map[string]strategystore.EngineAdapter {
	nfqws1 := NfqwsEngine{}
	nfqws2 := Nfqws2Engine{}
	return map[string]strategystore.EngineAdapter{
		nfqws1.Engine(): nfqws1,
		nfqws2.Engine(): nfqws2,
	}
}
