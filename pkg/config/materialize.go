package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/atomicfile"
)

// ManifestName is the file a MATERIALIZED deployment carries, recording where it
// came from. It lives in the target only; the source is authoritative and has none.
const ManifestName = ".lotsman-manifest.yaml"

// DocName and RulesDirName are the two halves of a deployment. They are named here
// because materialization moves both, and a rename that missed one would leave a
// target that loads as something other than what the source says.
const (
	DocName      = "configuration.yaml"
	RulesDirName = "rules"
)

// Manifest is the provenance record written next to a materialized deployment.
//
// It exists because a deployment can have a SOURCE and a TARGET, and the question
// "is the thing running the thing I edited?" is otherwise unanswerable. Two answers
// were considered and rejected: reading the target (which says nothing about whether
// it is current) and re-deriving it (which needs the source anyway). A recorded hash
// makes the pair checkable in both directions without either side being trusted.
type Manifest struct {
	Source         string    `yaml:"source"`
	SourceHash     string    `yaml:"source_hash"`
	MaterializedAt time.Time `yaml:"materialized_at"`
	Rules          int       `yaml:"rules"`
}

// MaterialState is the answer to "is the running deployment current with its
// source?", computed without writing anything. The GUI shows it; nothing acts on it
// automatically, because a service that silently re-materialized over an edited
// target would be a second writer to a file the operator may have changed by hand.
type MaterialState struct {
	Source         string    `json:"source"`
	Target         string    `json:"target"`
	SourceHash     string    `json:"source_hash"`
	AppliedHash    string    `json:"applied_hash"`
	Drifted        bool      `json:"drifted"`
	SourceMissing  bool      `json:"source_missing"`
	MaterializedAt time.Time `json:"materialized_at,omitempty"`
	Rules          int       `json:"rules"`
}

// DeployDir resolves a path to the DIRECTORY holding a deployment.
//
// It has to exist as a distinct concept rather than being folded into the loader's
// "directory or the document inside it", because a materialization TARGET does not
// exist yet: probing with os.Stat cannot tell a directory-to-be from a document
// path, and guessing wrong writes the document to a file where the tree belongs.
func DeployDir(p string) string {
	if p == "" {
		return ""
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return p
	}
	if ext := filepath.Ext(p); ext == ".yaml" || ext == ".yml" {
		return filepath.Dir(p)
	}
	return p
}

// DeployHash is a content hash of a deployment: the document plus every rule file,
// keyed by name. Two deployments with the same hash hold the same configuration,
// whatever order the files were written in or which directory they sit in.
//
// The manifest is deliberately excluded — it records the hash, so including it would
// make every deployment disagree with itself.
func DeployHash(dir string) (string, error) {
	dir = DeployDir(dir)
	doc := filepath.Join(dir, DocName)
	rulesDir := filepath.Join(dir, RulesDirName)
	entries := map[string][]byte{}

	data, err := os.ReadFile(doc)
	if err != nil {
		return "", err
	}
	entries[DocName] = data

	err = filepath.WalkDir(rulesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, ".yaml~") {
			return nil
		}
		if filepath.Base(p) == ManifestName {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		entries[d.Name()] = b
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash rules: %w", err)
	}

	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, n := range names {
		// Length-prefixed so no pair of names and contents can concatenate into the
		// same byte stream as a different pair.
		fmt.Fprintf(h, "%d:%s=%d:", len(n), n, len(entries[n]))
		h.Write(entries[n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Materialize copies a deployment from a user-writable SOURCE into a root-owned
// TARGET, and records what it copied.
//
// Both arguments are deployment DIRECTORIES, never a document path — see DeployDir
// for why that distinction is load-bearing rather than cosmetic.
//
// It refuses a source that does not load. That check is the whole reason this is a
// function in `pkg/config` rather than a `cp` in a start script: materializing a
// broken document over a working one is a downgrade, and the failure would otherwise
// appear only at the next service start, as a tunnel that does not come up.
//
// storeRoot is passed through to the same LoadV2 the service starts with. A second
// reader would mean a source that validates here and is rejected there, which is the
// class of bug preflight exists to catch.
func Materialize(src, dst, storeRoot string) (*Manifest, error) {
	src, dst = DeployDir(src), DeployDir(dst)
	srcDoc := filepath.Join(src, DocName)
	dstDoc := filepath.Join(dst, DocName)

	if _, err := LoadV2(src, "", storeRoot); err != nil {
		return nil, fmt.Errorf("source does not load, refusing to materialize: %w", err)
	}
	hash, err := DeployHash(src)
	if err != nil {
		return nil, err
	}

	// Build the whole tree beside the target, then swap it in. Writing rule files
	// into the live directory one at a time leaves a window where half the rules are
	// new and half are old, and a loader that reads a directory does not care that
	// those halves are from different moments.
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	staging := filepath.Join(dst, ".materializing")
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)

	rules, err := copyRules(filepath.Join(src, RulesDirName), staging)
	if err != nil {
		return nil, err
	}
	doc, err := os.ReadFile(srcDoc)
	if err != nil {
		return nil, err
	}
	if err := atomicfile.Write(filepath.Join(staging, DocName), doc, 0o600); err != nil {
		return nil, err
	}

	m := &Manifest{
		Source:         srcDoc,
		SourceHash:     hash,
		MaterializedAt: time.Now().UTC().Truncate(time.Second),
		Rules:          rules,
	}
	body, err := marshalManifest(m)
	if err != nil {
		return nil, err
	}

	// Swap the whole tree in one move: rename the staging directory over the target's
	// two halves. Doing it as two renames would leave a window where the document is
	// new and the rules are old, and a loader that reads a directory does not care
	// that those halves are from different moments.
	previous := filepath.Join(dst, ".previous")
	if err := os.RemoveAll(previous); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(previous, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(dstDoc); err == nil {
		if err := os.Rename(dstDoc, filepath.Join(previous, DocName)); err != nil {
			return nil, err
		}
		if err := os.Rename(filepath.Join(dst, RulesDirName), filepath.Join(previous, RulesDirName)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err := os.Rename(filepath.Join(staging, DocName), dstDoc); err != nil {
		// Put the old document back rather than leaving a target with no document,
		// which loads as nothing to start.
		if _, statErr := os.Stat(filepath.Join(previous, DocName)); statErr == nil {
			_ = os.Rename(filepath.Join(previous, DocName), dstDoc)
		}
		return nil, err
	}
	if err := os.Rename(filepath.Join(staging, RulesDirName), filepath.Join(dst, RulesDirName)); err != nil {
		// Restore both halves. A target holding a new document and no rules loads as
		// "every service gone", which is worse than not having tried.
		_ = os.Rename(dstDoc, filepath.Join(staging, DocName))
		if _, statErr := os.Stat(filepath.Join(previous, DocName)); statErr == nil {
			_ = os.Rename(filepath.Join(previous, DocName), dstDoc)
			_ = os.Rename(filepath.Join(previous, RulesDirName), filepath.Join(dst, RulesDirName))
		}
		return nil, err
	}
	// The manifest lands LAST: it is the claim "the target holds what the source
	// holds", and it must not exist on disk until that is true.
	if err := atomicfile.Write(filepath.Join(dst, ManifestName), body, 0o600); err != nil {
		return nil, err
	}
	return m, os.RemoveAll(previous)
}

// copyRules copies a rules directory into the staging root, returning how many rules
// it carried. A missing rules directory is an empty deployment, not an error: a
// document with no rules yet is a legitimate first state.
func copyRules(srcRules, staging string) (int, error) {
	dstRules := filepath.Join(staging, RulesDirName)
	if err := os.MkdirAll(dstRules, 0o700); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(srcRules)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".yaml~") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcRules, e.Name()))
		if err != nil {
			return n, err
		}
		if err := atomicfile.Write(filepath.Join(dstRules, e.Name()), data, 0o600); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// CheckMaterialized compares a source against a target without touching either.
//
// A missing manifest is reported as drift rather than as "never materialized", so a
// target an operator built by hand is not silently declared current.
func CheckMaterialized(src, dst string) MaterialState {
	src, dst = DeployDir(src), DeployDir(dst)
	srcDoc := filepath.Join(src, DocName)
	dstDoc := filepath.Join(dst, DocName)
	st := MaterialState{Source: srcDoc, Target: dst}

	srcHash, err := DeployHash(src)
	if err != nil {
		st.SourceMissing = true
		return st
	}
	st.SourceHash = srcHash
	if _, err := os.Stat(dstDoc); err != nil {
		st.Drifted = true
		return st
	}
	body, err := os.ReadFile(filepath.Join(dst, ManifestName))
	if err != nil {
		st.Drifted = true
		return st
	}
	m, err := parseManifest(body)
	if err != nil {
		st.Drifted = true
		return st
	}
	st.AppliedHash = m.SourceHash
	st.MaterializedAt = m.MaterializedAt
	st.Rules = m.Rules
	st.Drifted = m.SourceHash != srcHash
	return st
}

func marshalManifest(m *Manifest) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Written by lotsman. Records where this deployment was materialized FROM.\n")
	b.WriteString("# The source is authoritative; this directory is derived from it.\n")
	fmt.Fprintf(&b, "source: %s\n", m.Source)
	fmt.Fprintf(&b, "source_hash: %s\n", m.SourceHash)
	fmt.Fprintf(&b, "materialized_at: %s\n", m.MaterializedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "rules: %d\n", m.Rules)
	return []byte(b.String()), nil
}

func parseManifest(body []byte) (*Manifest, error) {
	var m Manifest
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "source":
			m.Source = v
		case "source_hash":
			m.SourceHash = v
		case "rules":
			_, _ = fmt.Sscanf(v, "%d", &m.Rules)
		case "materialized_at":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				m.MaterializedAt = t
			}
		}
	}
	if m.SourceHash == "" {
		return nil, fmt.Errorf("manifest has no source_hash")
	}
	return &m, nil
}

// DocumentPath resolves a deployment path to the DOCUMENT inside it.
//
// It exists because two things accept "a directory or the document in it" — the
// loader and the control server's config editor — and the second one reads the path
// as a file. Two resolvers for one path shape is how they drift, and they had: the
// editor returned "is a directory" on exactly the deployment shape v2 installs.
func DocumentPath(p string) string {
	if p == "" {
		return ""
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return filepath.Join(p, DocName)
	}
	return p
}
