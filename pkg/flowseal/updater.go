package flowseal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// Release is the parsed subset of a GitHub release we need.
type Release struct {
	Tag    string
	ZipURL string
}

// ParseRelease extracts the tag and the .zip asset URL from a GitHub releases
// API JSON body (the /releases/latest response).
func ParseRelease(body []byte) (Release, error) {
	var r struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Release{}, fmt.Errorf("flowseal: release json: %w", err)
	}
	rel := Release{Tag: r.TagName}
	for _, a := range r.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".zip") {
			rel.ZipURL = a.URL
			break
		}
	}
	if rel.Tag == "" {
		return Release{}, fmt.Errorf("flowseal: no tag_name in release")
	}
	return rel, nil
}

// Installer performs the side effects of an update. Abstracted so the updater's
// decision logic is testable without touching disk/network.
type Installer interface {
	CurrentVersion() string                                     // from the flowseal-current symlink, "" if none
	Install(ctx context.Context, rel Release, raw []byte) error // unzip into versioned dir + repoint symlink
}

const releaseAPI = "https://api.github.com/repos/Flowseal/zapret-discord-youtube/releases/latest"

// Updater checks for and applies Flowseal updates.
type Updater struct {
	fetcher subscription.Fetcher
	inst    Installer
}

// NewUpdater builds an updater.
func NewUpdater(f subscription.Fetcher, inst Installer) *Updater {
	return &Updater{fetcher: f, inst: inst}
}

// Outcome reports what CheckAndUpdate did.
type Outcome struct {
	Current string
	Latest  string
	Updated bool
}

// CheckAndUpdate fetches the latest release, and if it is newer than the
// installed version, downloads and installs it. It is a no-op (Updated=false)
// when already current.
func (u *Updater) CheckAndUpdate(ctx context.Context) (Outcome, error) {
	out := Outcome{Current: u.inst.CurrentVersion()}

	body, err := u.fetcher.Fetch(ctx, releaseAPI)
	if err != nil {
		return out, fmt.Errorf("flowseal: fetch release: %w", err)
	}
	rel, err := ParseRelease(body)
	if err != nil {
		return out, err
	}
	out.Latest = rel.Tag

	if out.Current != "" && !Newer(rel.Tag, out.Current) {
		return out, nil // already up to date
	}
	if rel.ZipURL == "" {
		return out, fmt.Errorf("flowseal: release %s has no zip asset", rel.Tag)
	}

	raw, err := u.fetcher.Fetch(ctx, rel.ZipURL)
	if err != nil {
		return out, fmt.Errorf("flowseal: download %s: %w", rel.ZipURL, err)
	}
	if err := u.inst.Install(ctx, rel, raw); err != nil {
		return out, fmt.Errorf("flowseal: install %s: %w", rel.Tag, err)
	}
	out.Updated = true
	return out, nil
}
