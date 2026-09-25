package strategystore

import (
	"os"

	"github.com/strace-me/lotsman/pkg/atomicfile"
)

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
