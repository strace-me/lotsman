package strategystore

type PrepareOptions struct {
	QNum    int
	Fwmark  int
	WorkDir string
	// WriteableDir is where an engine may put what its LUA scripts generate.
	// nfqws2 takes it as --writeable; nfqws1 has no equivalent and ignores it.
	WriteableDir string
}

type EngineAdapter interface {
	Engine() string
	Validate(Bundle) error
	Prepare(Bundle, PrepareOptions) ([]string, error)
}
