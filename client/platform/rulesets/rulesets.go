// Package rulesets provisions the sing-box .srs rule-set files a client config
// references. On the router these are unpacked into /etc/sing-box by the daemon's
// updater (pkg/rulesets); a desktop client has no such thing, so it fetches the
// SAME upstream release bundle and extracts only the tags its config actually
// uses — a few hundred KB out of a ~27 MB archive.
package rulesets

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// BundleURL is the upstream sing-box rule-set bundle — the same source the
// router's updater uses, so a tag resolves to identical data on both.
const BundleURL = "https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/sing-box.zip"

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
func Ensure(ctx context.Context, dir string, tags []string, log *slog.Logger) error {
	want := map[string]string{} // bundle-relative path -> destination
	for _, tag := range tags {
		rel, ok := RelPath(tag)
		if !ok {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(dst); err == nil {
			continue // already provisioned
		}
		want[rel] = dst
	}
	if len(want) == 0 {
		return nil
	}
	log.Info("provisioning sing-box rule-sets", "missing", len(want), "dir", dir)

	tmp, err := os.CreateTemp("", "lotsman-rulesets-*.zip")
	if err != nil {
		return fmt.Errorf("rulesets: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BundleURL, nil)
	if err != nil {
		return fmt.Errorf("rulesets: request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("rulesets: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rulesets: download: %s", resp.Status)
	}
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return fmt.Errorf("rulesets: download body: %w", err)
	}

	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return fmt.Errorf("rulesets: open bundle: %w", err)
	}
	for _, f := range zr.File {
		dst, ok := want[f.Name]
		if !ok {
			continue
		}
		if err := extract(f, dst); err != nil {
			return err
		}
		delete(want, f.Name)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for rel := range want {
			missing = append(missing, rel)
		}
		return fmt.Errorf("rulesets: bundle lacks %v", missing)
	}
	log.Info("rule-sets provisioned", "dir", dir)
	return nil
}

// extract writes one bundle entry to dst atomically (tmp + rename), so a killed
// download can never leave a truncated .srs that sing-box would refuse to parse.
func extract(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("rulesets: mkdir: %w", err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("rulesets: open %s: %w", f.Name, err)
	}
	defer rc.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("rulesets: create %s: %w", tmp, err)
	}
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
