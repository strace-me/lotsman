package zapret

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategystore"
)

// autoBlobPrefix is the namespace nfqws2 fills itself. A bundle that names one of
// these is refused by the engine with `duplicate blob name` — verified on
// v0.9.5.2, which prints exactly that for `--blob=fake_default_tls:@file` even
// though the file is present and readable. So these must not be resolved to a
// file: there is no file to find, and the engine already has the contents.
const autoBlobPrefix = "fake_default_"

// blobRef is one parsed `--blob=` value.
type blobRef struct {
	Name string
	// File is the referenced file for the `@path` form, "" otherwise.
	File string
	// Inline is true for the `0xHEX` form, which needs no file.
	Inline bool
}

// blobRefs parses the `--blob=` arguments of a bundle.
func blobRefs(b strategystore.Bundle) ([]blobRef, error) {
	var out []blobRef
	for _, arg := range b.Args.Values {
		raw, ok := strings.CutPrefix(arg, "--blob=")
		if !ok {
			continue
		}
		name, value, ok := strings.Cut(raw, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("nfqws2 strategy %q has a --blob with no name", b.Manifest.ID)
		}
		value = strings.TrimSpace(value)
		switch {
		case value == "":
			return nil, fmt.Errorf("nfqws2 strategy %q has --blob=%s with no value; it wants 0xHEX or @file", b.Manifest.ID, name)
		case strings.HasPrefix(value, "@"):
			out = append(out, blobRef{Name: name, File: strings.TrimPrefix(value, "@")})
		case strings.HasPrefix(value, "0x"), strings.HasPrefix(value, "0X"):
			out = append(out, blobRef{Name: name, Inline: true})
		default:
			return nil, fmt.Errorf("nfqws2 strategy %q has --blob=%s:%s; only 0xHEX or @file are accepted", b.Manifest.ID, name, value)
		}
	}
	return out, nil
}

// AbsolutizeBlobs rewrites `@relative` blob paths to absolute ones under dir, the
// way nfqws1's payloads are resolved. nfqws2 resolves a relative name against its
// working directory, which is ours, so a bare filename is found by accident or not
// at all. A path that is already absolute, or an inline hex blob, is left alone.
func (Nfqws2Engine) AbsolutizeBlobs(b strategystore.Bundle, dir string) ([]string, error) {
	refs, err := blobRefs(b)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return b.Args.Values, nil
	}
	out := make([]string, 0, len(b.Args.Values))
	for _, arg := range b.Args.Values {
		raw, ok := strings.CutPrefix(arg, "--blob=")
		if !ok {
			out = append(out, arg)
			continue
		}
		var match *blobRef
		for i := range refs {
			if strings.HasPrefix(raw, refs[i].Name+":") {
				match = &refs[i]
				break
			}
		}
		if match == nil || match.Inline || match.File == "" || filepath.IsAbs(match.File) {
			out = append(out, arg)
			continue
		}
		out = append(out, "--blob="+match.Name+":@"+filepath.Join(dir, filepath.Base(match.File)))
	}
	return out, nil
}

// CheckBlobs reports blobs this host cannot load, so a recipe is kept out of the
// candidate set instead of taking the engine down at launch. The engine refuses a
// missing file itself ("cannot access file '/nope/x.bin'"), but that happens on
// START: the recipe would be chosen, accepted, and take the desync with it.
func (Nfqws2Engine) CheckBlobs(b strategystore.Bundle, dir string) error {
	refs, err := blobRefs(b)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Inline {
			continue // 0xHEX carries its own bytes
		}
		if strings.HasPrefix(ref.Name, autoBlobPrefix) {
			return fmt.Errorf("nfqws2 strategy %q sets blob %q, which the engine initialises itself; naming it again is a duplicate", b.Manifest.ID, ref.Name)
		}
		path := ref.File
		if dir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(dir, filepath.Base(path))
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("nfqws2 strategy %q needs blob file %s: %w", b.Manifest.ID, path, err)
		}
	}
	return nil
}
