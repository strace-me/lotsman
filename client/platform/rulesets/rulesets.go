// Package rulesets provisions the sing-box .srs rule-set files a client config
// references. On the router these are unpacked into /etc/sing-box by the daemon's
// updater (pkg/rulesets); a desktop client has no such thing, so it fetches the
// SAME upstream release bundle and extracts only the tags its config actually
// uses — a few hundred KB out of a ~27 MB archive.
package rulesets

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// Aliased: this package and the daemon's updater are both "rulesets", and the
	// shared pieces — Snapshot, SingboxCounter — are the daemon's, so that a client and
	// a router cannot drift on what a snapshot looks like or how a rule-set is counted.
	rulesets "github.com/strace-me/lotsman/pkg/rulesets"
)

// DefaultRepo is the upstream sing-box rule-set bundle — the same source the
// router's updater uses, so a tag resolves to identical data on both.
//
// It is a DEFAULT, not the answer: a v2 document names its own data source and the
// repo comes from there. It was a hardcoded constant, which meant a fork or a pinned
// mirror was impossible on the client without editing code, and the client's copy
// could quietly drift from the router's.
const DefaultRepo = "runetfreedom/russia-v2ray-rules-dat"

// BundleURL is the release asset for a repo, following latest.
func BundleURL(repo string) string {
	if repo == "" {
		repo = DefaultRepo
	}
	return "https://github.com/" + repo + "/releases/latest/download/sing-box.zip"
}

// BundleURLAt is the release asset for an explicit tag, so a document that pins a
// release gets that release rather than whatever is newest.
func BundleURLAt(repo, tag string) string {
	if repo == "" {
		repo = DefaultRepo
	}
	if tag == "" {
		return BundleURL(repo)
	}
	return "https://github.com/" + repo + "/releases/download/" + tag + "/sing-box.zip"
}

// RelPath maps a rule-set tag to its path inside the bundle, which is also its
// path under the local rule-set dir — mirroring what pkg/singbox emits for a
// local rule_set entry. Unknown prefixes are skipped, not guessed.
func RelPath(tag string) (string, bool) {
	switch {
	case strings.HasPrefix(tag, "geosite-"):
		return "rule-set-geosite/" + tag + ".srs", true
	case strings.HasPrefix(tag, "geoip-"):
		return "rule-set-geoip/" + tag + ".srs", true
	}
	return "", false
}

// Ensure guarantees every tag exists under dir, downloading and extracting the
// upstream bundle only when something is missing. A warm cache is a no-op, so
// this is safe to call on every start.
// bundleClient downloads the rule-set bundle with deadlines of its own.
//
// http.DefaultClient has NO timeout, and this runs inside Start, before the control
// socket is even bound. Behind a captive portal or on a half-associated tether the
// connection is accepted and then goes quiet, so the download hangs forever: no tunnel,
// no socket, no way to ask what is wrong — and systemd reports the unit as active
// (running), because from its side the process is alive and doing something. A bounded
// failure that retries is strictly better than an unbounded wait that looks healthy.
//
// The overall Timeout covers the whole body read, not just the handshake, because the
// failure mode here is a stalled transfer rather than a refused connection. It is
// generous: the bundle is ~27 MB and some of the users this ships for are on slow links.
var bundleClient = &http.Client{
	Timeout: 5 * time.Minute,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// Source is where the client's bundle comes from. It mirrors the daemon's document
// data source so one config can drive both roots.
type Source struct {
	Repo string
	Pin  string
	URL  string // explicit asset URL; wins over Repo/Pin
}

// EnsureFrom is Ensure with the source named, so a v2 document is authoritative on
// the client exactly as it is on the router.
func EnsureFrom(ctx context.Context, src Source, dir string, tags []string, log *slog.Logger) error {
	return EnsureFromCounter(ctx, src, dir, tags, UnreadableCounter{}, log)
}

// EnsureFromCounter is EnsureFrom with a rule-set counter, which is what turns
// "the file exists" into "the file is readable" and makes a truncated rule-set a
// repair instead of a permanent silent skip.
func EnsureFromCounter(ctx context.Context, src Source, dir string, tags []string, counter Counter, log *slog.Logger) error {
	url := src.URL
	if url == "" {
		url = BundleURLAt(src.Repo, src.Pin)
	}
	return ensureFromURL(ctx, url, dir, tags, counter, log)
}

func Ensure(ctx context.Context, dir string, tags []string, log *slog.Logger) error {
	return ensureDefault(ctx, dir, tags, log)
}

// Counter reports how many rules a .srs holds, or an error if it cannot be read.
//
// It is an interface because the client's own sing-box binary is what can decompile
// one, and hardcoding a path here would mean a second answer to "where is sing-box".
type Counter interface {
	Count(srsPath string) (int, error)
}

// UnreadableCounter stands in when no sing-box binary is available. It never reports a
// file as broken, so provisioning falls back to "present means present" — which is
// the old behaviour, and is why the repair path below needs a real counter to be worth
// having.
type UnreadableCounter struct{}

func (UnreadableCounter) Count(string) (int, error) { return 0, errNoCounter }

// The counter that decompiles, matching the daemon's own.
type SingboxCounter struct{ Bin string }

func (c SingboxCounter) Count(p string) (int, error) {
	return rulesets.SingboxCounter{Bin: c.Bin}.Count(p)
}

var errNoCounter = errors.New("rulesets: no sing-box binary to verify rule-sets with")

// ensureDefault is what callers that have no sing-box binary use.
func ensureDefault(ctx context.Context, dir string, tags []string, log *slog.Logger) error {
	return ensureFromURL(ctx, BundleURL(""), dir, tags, UnreadableCounter{}, log)
}

func ensureFromURL(ctx context.Context, bundleURL, dir string, tags []string, counter Counter, log *slog.Logger) error {
	want := map[string]string{} // bundle-relative path -> destination
	repairTag := map[string]string{}
	var repair []string
	for _, tag := range tags {
		rel, ok := RelPath(tag)
		if !ok {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(dst); err != nil {
			want[rel] = dst
			continue
		}
		// The file EXISTS, which is not the same as it being usable. An extract cut
		// short by a reboot leaves a truncated .srs that sing-box then loads and either
		// rejects at check or, worse, matches nothing — and because the name is present,
		// "already provisioned" skipped it on every boot after. A present-but-unreadable
		// rule-set is a REPAIR, not a skip.
		if _, err := counter.Count(dst); err != nil {
			repair = append(repair, dst)
			// The TAG, not the file name: relPathFor turns a tag into a path, and
			// handing it "geosite-youtube.srs" produced a restore looking for
			// "geosite-youtube.srs.srs" — a rollback that fails at the exact moment
			// it is needed.
			repairTag[dst] = tag
			want[rel] = dst
		}
	}
	if len(want) == 0 {
		return nil
	}
	// Snapshot what is about to be overwritten. The client had no rollback point and
	// the daemon's is what survives a bad release; a laptop gets a truncated file
	// exactly as often as a router does, usually by being closed mid-download.
	// Only when something is actually about to be OVERWRITTEN. A first provision has
	// nothing to lose, and an empty snapshot directory is a lie about a rollback point
	// that does not exist.
	var snap string
	if len(repair) > 0 {
		var err error
		snap, err = rulesets.Snapshot(dir, repair, filepath.Join(dir, "snapshots"),
			relPathFor, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("rulesets: snapshot before repair: %w", err)
		}
		log.Info("rule-sets: snapshot before repair", "files", len(repair), "snapshot", snap)
	}
	rollback := func(cause error) error {
		if snap == "" {
			// Say WHICH case it is. "restored" on a first provision would report a
			// rollback that never happened, and a reader would assume their old
			// rule-sets are back when nothing was ever overwritten.
			if len(repair) == 0 {
				return fmt.Errorf("rulesets: provision: %w (nothing to restore: this was a first provision)", cause)
			}
			return fmt.Errorf("rulesets: %d rule-set(s) are unreadable and there is no snapshot: %w", len(repair), cause)
		}
		if rerr := restore(snap, repairTag); rerr != nil {
			return fmt.Errorf("rulesets: %w (and the restore from %s failed: %v)", cause, snap, rerr)
		}
		return fmt.Errorf("rulesets: %w (restored %d file(s) from %s)", cause, len(repair), snap)
	}
	log.Info("provisioning sing-box rule-sets", "missing", len(want), "dir", dir, "repairing", len(repair))

	tmp, err := os.CreateTemp("", "lotsman-rulesets-*.zip")
	if err != nil {
		return rollback(fmt.Errorf("temp file: %w", err))
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bundleURL, nil)
	if err != nil {
		return rollback(fmt.Errorf("request: %w", err))
	}
	resp, err := bundleClient.Do(req)
	if err != nil {
		return rollback(fmt.Errorf("download: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rollback(fmt.Errorf("download: %s", resp.Status))
	}
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return rollback(fmt.Errorf("download body: %w", err))
	}

	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return rollback(fmt.Errorf("open bundle: %w", err))
	}
	// Extract to a staging file and rename, so a failure part-way through cannot leave
	// a half-written rule-set where a working one used to be. The daemon gets this from
	// the reconciler; the client writes straight to the destination, which is how a
	// killed process becomes a silent permanent outage on the next boot.
	for _, f := range zr.File {
		dst, ok := want[f.Name]
		if !ok {
			continue
		}
		if err := stageThenRename(f, dst); err != nil {
			return rollback(err)
		}
		delete(want, f.Name)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for rel := range want {
			missing = append(missing, rel)
		}
		sort.Strings(missing)
		return rollback(fmt.Errorf("bundle lacks %v", missing))
	}
	// Count what landed, so a bundle that arrived EMPTY is refused here rather than
	// discovered as a rule that stops matching.
	for _, dst := range repair {
		if c, err := counter.Count(dst); err != nil {
			return rollback(fmt.Errorf("repaired %s is still unreadable: %w", dst, err))
		} else if c == 0 {
			return rollback(fmt.Errorf("repaired %s contains no rules", dst))
		}
	}
	log.Info("rule-sets provisioned", "dir", dir, "repaired", len(repair))
	return nil
}

// relPathFor is the daemon's name for "where does this tag live under the live dir",
// shared so the snapshot layout and the provisioning layout cannot disagree.
func relPathFor(tag string) (string, bool) { return RelPath(tag) }

// stageThenRename extracts into the destination's directory and renames it into place,
// so the destination only ever holds a complete file.
func stageThenRename(f *zip.File, dst string) error {
	stage, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return fmt.Errorf("rulesets: stage %s: %w", dst, err)
	}
	defer os.Remove(stage.Name())
	if err := extract(f, stage.Name()); err != nil {
		stage.Close()
		return err
	}
	if err := stage.Sync(); err != nil {
		stage.Close()
		return fmt.Errorf("rulesets: sync %s: %w", dst, err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("rulesets: close %s: %w", dst, err)
	}
	if err := os.Chmod(stage.Name(), 0o644); err != nil {
		return fmt.Errorf("rulesets: chmod %s: %w", dst, err)
	}
	return os.Rename(stage.Name(), dst)
}

// restore puts the snapshotted copies back over the destinations. It takes the TAG
// each destination came from, because that is what relPathFor consumes.
func restore(snap string, tagOf map[string]string) error {
	var firstErr error
	for dst, tag := range tagOf {
		rel, ok := relPathFor(tag)
		if !ok {
			continue
		}
		if err := copyFile(filepath.Join(snap, filepath.FromSlash(rel)), dst); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func extract(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("rulesets: mkdir: %w", err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("rulesets: open %s: %w", f.Name, err)
	}
	defer rc.Close()

	// A UNIQUE temp name, not dst+".tmp": two writers of one destination would
	// interleave into the same temporary file, which is the defect pkg/atomicfile
	// exists to remove. This one is hand-rolled because the bytes come out of a zip
	// reader rather than out of memory.
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return fmt.Errorf("rulesets: create staging file for %s: %w", dst, err)
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("rulesets: write %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rulesets: close %s: %w", tmp, err)
	}
	return os.Rename(tmp, dst)
}
