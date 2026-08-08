// Command presetgen converts an unpacked upstream bundle's Windows launchers
// into nfqws .args files Lotsman runs verbatim.
//
// The reading is pkg/strategyimport's, the same one that builds catalog recipes:
// one parser over the bundle, two renderings of it.
//
// It exists so the conversion is repeatable. The catalogue holds ONE of
// Flowseal's twenty-one strategies because transcribing them by hand is work
// nobody does twice; regenerating against a newer release should be a diff.
//
//	go run ./cmd/presetgen -src <unpacked release> -out docs/presets -version 1.10.0
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategyimport"
)

func main() {
	src := flag.String("src", "", "unpacked upstream release directory (holds the .bat launchers)")
	out := flag.String("out", "docs/presets", "directory to write .args into")
	version := flag.String("version", "", "upstream version, recorded in each file's provenance")
	lists := flag.String("lists", "/var/lib/lotsman-hostlists/flowseal",
		"where the bundle's own lists will live on the target box")
	flag.Parse()
	if *src == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "presetgen: -src and -version are required")
		os.Exit(2)
	}
	bats, err := filepath.Glob(filepath.Join(*src, "*.bat"))
	if err != nil || len(bats) == 0 {
		fmt.Fprintf(os.Stderr, "presetgen: no .bat launchers under %s\n", *src)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "presetgen:", err)
		os.Exit(1)
	}
	written, skipped := 0, 0
	for _, f := range bats {
		// service.bat is the bundle's own plumbing, not a strategy.
		if strings.EqualFold(filepath.Base(f), "service.bat") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  read %s: %v\n", filepath.Base(f), err)
			skipped++
			continue
		}
		name := strategyimport.PresetName(f)
		p, err := strategyimport.AsPreset(name,
			strategyimport.Source{Kind: strategyimport.KindBat, Body: data, Name: filepath.Base(f)},
			strategyimport.PresetOptions{ListsDir: *lists})
		if err != nil {
			// Named, not swallowed: a launcher this cannot render is a gap in the
			// converter, and a silent skip would leave the operator believing the
			// directory holds every strategy.
			fmt.Fprintf(os.Stderr, "  SKIP %s: %v\n", name, err)
			skipped++
			continue
		}
		dst := filepath.Join(*out, "flowseal-"+slug(name)+".args")
		body := strategyimport.RenderPreset(p, filepath.Base(*src)+" "+*version+", "+filepath.Base(f))
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "presetgen:", err)
			os.Exit(1)
		}
		fmt.Printf("%-22s args=%-4d lists=%s\n", name, len(p.Args), strings.Join(p.Lists, " "))
		written++
	}
	fmt.Printf("written %d, skipped %d\n", written, skipped)
	if skipped > 0 {
		os.Exit(1)
	}
}

func slug(name string) string {
	return strings.ToLower(strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(name))
}
