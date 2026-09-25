package strategystore

type PrepareOptions struct {
	QNum    int
	Fwmark  int
	WorkDir string
}

type EngineAdapter interface {
	Engine() string
	Validate(Bundle) error
	Prepare(Bundle, PrepareOptions) ([]string, error)
}
