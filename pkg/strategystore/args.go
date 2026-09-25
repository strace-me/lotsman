package strategystore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Args struct {
	Name   string
	Values []string
}

func ParseArgs(data []byte, declaredName, fileName string, single bool) (Args, error) {
	name := ""
	values := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if name == "" {
				if rest, ok := strings.CutPrefix(line, "# "); ok {
					if i := strings.Index(rest, " — "); i > 0 {
						name = strings.TrimSpace(rest[:i])
					}
				}
			}
			continue
		}
		values = append(values, line)
	}
	if len(values) == 0 {
		return Args{}, fmt.Errorf("args file %q holds no arguments", fileName)
	}
	if single && declaredName != "" {
		name = declaredName
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(fileName), ".args")
	}
	return Args{Name: name, Values: values}, nil
}

func ReadArgs(file, declaredName string, single bool) (Args, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Args{}, fmt.Errorf("args file %q: %w", file, err)
	}
	parsed, err := ParseArgs(data, declaredName, file, single)
	if err != nil {
		return Args{}, fmt.Errorf("args file %q: %w", file, err)
	}
	return parsed, nil
}

func WriteArgs(file string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("args file %q holds no arguments", file)
	}
	return writeFileAtomic(file, []byte(strings.Join(values, "\n")+"\n"), 0o644)
}
