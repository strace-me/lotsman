package rulesets

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Releaser resolves and fetches a rule-set release. Real impl talks to GitHub;
// tests fake it. Fetch returns a temp dir with the extracted release (the same
// rule-set-{geosite,geoip}/ layout as LiveDir) plus a cleanup func.
type Releaser interface {
	Latest(ctx context.Context, repo string) (tag string, err error)
	Fetch(ctx context.Context, repo, tag string) (dir string, cleanup func(), err error)
}

// Updater runs one Track-A cycle: resolve the release to use (pin or autobumped
// latest), fetch it, count the required tags in candidate vs live, decide per-tag
// swaps (shrink-guarded), apply, and trigger a reconcile. Validation-before-apply
// is the shrink guard + the reconcile's own `sing-box check`.
type Updater struct {
	Repo      string   // e.g. "runetfreedom/russia-v2ray-rules-dat"
	Required  []string // rule-set tags the running config references
	LiveDir   string   // where the .srs live (singbox.Options.RuleSetDir)
	Pin       string   // pinned release tag ("" = track latest)
	AutoBump  bool     // evaluate latest even when pinned; move the pin only if it validates
	MinRatio  float64  // shrink guard
	PerTag    bool     // per-tag vs global shrink mode
	DryRun    bool
	Counter   Counter
	Releaser  Releaser
	PathFor   PathFor
	OnApplied func(ctx context.Context) error // e.g. trigger reconcile (re-validate + apply)
	Log       *slog.Logger

	pin string // in-memory current pin (seeded from Pin)
}

// Update performs one cycle. It never returns an error for a release it chose to
// reject (keep-old) — only for infrastructure failures (resolve/fetch/apply).
func (u *Updater) Update(ctx context.Context) error {
	if u.pin == "" {
		u.pin = u.Pin
	}
	latest, err := u.Releaser.Latest(ctx, u.Repo)
	if err != nil {
		return fmt.Errorf("rulesets: resolve latest: %w", err)
	}
	// Which release to evaluate this cycle.
	candidate := u.pin
	if candidate == "" || u.AutoBump {
		candidate = latest
	}
	if candidate == "" {
		return fmt.Errorf("rulesets: no release to evaluate (no pin, no latest)")
	}

	dir, cleanup, err := u.Releaser.Fetch(ctx, u.Repo, candidate)
	if err != nil {
		return fmt.Errorf("rulesets: fetch %s: %w", candidate, err)
	}
	defer cleanup()

	cand := u.countDir(dir)
	live := u.countDir(u.LiveDir)
	plan := PlanSwap(u.Required, cand, live, u.MinRatio, u.PerTag)

	for tag, s := range plan.KeepOld {
		u.Log.Warn("rulesets: keeping old tag (shrank/vanished)", "tag", tag, "prev", s.Prev, "next", s.Next)
	}
	if plan.Unusable {
		u.Log.Warn("rulesets: release unusable, nothing changed", "tag", candidate, "missing", plan.Missing)
		return nil
	}
	if u.DryRun {
		u.Log.Info("rulesets: dry-run", "tag", candidate, "would_swap", plan.Swap, "keep_old", len(plan.KeepOld))
		return nil
	}

	changed, err := Apply(plan, dir, u.LiveDir, u.PathFor)
	if err != nil {
		return fmt.Errorf("rulesets: apply: %w", err)
	}
	if newPin, bumped := NextPin(u.pin, latest, !plan.Unusable); bumped {
		u.Log.Info("rulesets: pin bumped", "from", u.pin, "to", newPin)
		u.pin = newPin
	}
	if len(changed) == 0 {
		u.Log.Info("rulesets: up to date", "tag", candidate)
		return nil
	}
	u.Log.Info("rulesets: updated tags", "tag", candidate, "changed", changed)
	if u.OnApplied != nil {
		if err := u.OnApplied(ctx); err != nil {
			return fmt.Errorf("rulesets: post-apply reconcile: %w", err)
		}
	}
	return nil
}

// countDir counts the required tags' .srs in dir (missing => absent from the map,
// which PlanSwap reads as not-present).
func (u *Updater) countDir(dir string) map[string]int {
	out := make(map[string]int, len(u.Required))
	for _, tag := range u.Required {
		rel, ok := u.PathFor(tag)
		if !ok {
			continue
		}
		if n, err := u.Counter.Count(filepath.Join(dir, rel)); err == nil {
			out[tag] = n
		}
	}
	return out
}

// --- real implementations (run on the box) ---

// SingboxCounter counts entries in a .srs via `sing-box rule-set decompile`.
type SingboxCounter struct {
	Bin string // sing-box binary ("" => "sing-box")
}

// Count decompiles the .srs to JSON and sums its match arrays.
func (c SingboxCounter) Count(srsPath string) (int, error) {
	if _, err := os.Stat(srsPath); err != nil {
		return 0, err
	}
	bin := c.Bin
	if bin == "" {
		bin = "sing-box"
	}
	tmp, err := os.CreateTemp("", "lotsman-srs-*.json")
	if err != nil {
		return 0, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "rule-set", "decompile", srsPath, "-o", tmp.Name()).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("decompile %s: %w: %s", srsPath, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return 0, err
	}
	var doc struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("parse decompiled %s: %w", srsPath, err)
	}
	matchKeys := []string{"domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr"}
	total := 0
	for _, rule := range doc.Rules {
		for _, k := range matchKeys {
			if raw, ok := rule[k]; ok {
				var arr []json.RawMessage
				if json.Unmarshal(raw, &arr) == nil {
					total += len(arr)
				}
			}
		}
	}
	return total, nil
}

// GitHubReleaser resolves + downloads a release's sing-box.zip asset.
type GitHubReleaser struct {
	Client *http.Client // nil => default with a timeout
	Asset  string       // asset filename ("" => "sing-box.zip")
}

func (g GitHubReleaser) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Latest reads the latest release tag via the GitHub API.
func (g GitHubReleaser) Latest(ctx context.Context, repo string) (string, error) {
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := g.getJSON(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", &rel); err != nil {
		return "", err
	}
	return rel.Tag, nil
}

// Fetch downloads the sing-box.zip of tag and extracts it to a temp dir.
func (g GitHubReleaser) Fetch(ctx context.Context, repo, tag string) (string, func(), error) {
	asset := g.Asset
	if asset == "" {
		asset = "sing-box.zip"
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := g.client().Do(req)
	if err != nil {
		return "", func() {}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	tmpZip, err := os.CreateTemp("", "lotsman-rel-*.zip")
	if err != nil {
		return "", func() {}, err
	}
	defer os.Remove(tmpZip.Name())
	if _, err := io.Copy(tmpZip, resp.Body); err != nil {
		tmpZip.Close()
		return "", func() {}, err
	}
	tmpZip.Close()

	dir, err := os.MkdirTemp("", "lotsman-rs-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	if err := unzip(tmpZip.Name(), dir); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dir, cleanup, nil
}

func (g GitHubReleaser) getJSON(ctx context.Context, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// unzip extracts a zip into dir, guarding against path traversal (Zip Slip).
func unzip(src, dir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		dst := filepath.Join(dir, f.Name)
		if !strings.HasPrefix(dst, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("unzip: illegal path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(dst, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
