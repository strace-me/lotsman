package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/strace-me/lotsman/client/control"
	"github.com/strace-me/lotsman/client/scaffold"
	"github.com/strace-me/lotsman/pkg/atomicfile"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/version"
)

// App is the Wails backend. It is a deliberately thin pass-through to the control
// socket: every method just forwards to client/control, so all state and policy
// stay in the service and the GUI only renders + issues actions. Wails binds these
// exported methods to the frontend (window.go.main.App.*).
type App struct {
	ctx    context.Context
	client *control.Client
}

// NewApp builds the backend against socket (empty = the default runtime path).
func NewApp(socket string) *App {
	if socket == "" {
		socket = control.DefaultSocketPath()
	}
	return &App{client: control.New(socket)}
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// OwnVersion is THIS WINDOW's build identity, stamped by scripts/build.sh into
// pkg/version like every other target.
//
// The bar shows the SERVICE's version, because what a reader needs first is which
// build is answering. But the GUI is the one target build.sh does not build — it
// needs wails and a webkit toolchain — so it is built by hand, separately, and it
// drifts. On 2026-08-07 the window on the owner's laptop was two commits older
// than its service, and the only thing that gave it away was a hint sentence he
// happened to screenshot. A drift nobody can see is a drift nobody fixes.
func (a *App) OwnVersion() string { return version.String() }

// Status returns the rich /status the whole UI renders from.
func (a *App) Status() (control.Report, error) { return a.client.Status(a.ctx) }

// Events returns recent brain transitions newest-first (the "история событий").
func (a *App) Events(limit int, service string) ([]control.Event, error) {
	return a.client.Events(a.ctx, limit, service)
}

// Recheck forces an immediate probe of one service ("перепроверить").
func (a *App) Recheck(service string) error { return a.client.Recheck(a.ctx, service) }

// SetServiceEnabled switches one service on or off (the tile toggle). The daemon
// writes the bit to the config file, so it survives a restart and is not undone by
// the next reassert.
func (a *App) SetServiceEnabled(service string, enabled bool) error {
	return a.client.SetServiceEnabled(a.ctx, service, enabled)
}

// Stop is the master OFF: it tears the service down.
func (a *App) Stop() error { return a.client.Stop(a.ctx) }

// RefreshSubscriptions re-fetches subscriptions and applies the result; an empty
// name does all of them. It returns what EACH one yielded, because "why did the
// one I just added give me nothing" is the question the button exists to answer —
// and until now the answer lived only in a log that rotates in six minutes.
//
// Not SetConfig: that rebuilds the whole autonomy loop to re-read a file that has
// not changed. This runs the same reconcile the refresh timer runs.
func (a *App) RefreshSubscriptions(name string) ([]control.SubRefresh, error) {
	return a.client.RefreshSubscriptions(a.ctx, name)
}

// Config returns the service's current config file (raw YAML + its path).
func (a *App) Config() (control.ConfigDoc, error) { return a.client.Config(a.ctx) }

// ValidateConfig checks a candidate raw-YAML config without writing it.
func (a *App) ValidateConfig(yaml string) error { return a.client.ValidateConfig(a.ctx, yaml) }

// SetConfig validates, writes and applies a raw-YAML config — the escape hatch.
func (a *App) SetConfig(yaml string) error { return a.client.SetConfig(a.ctx, yaml) }

// ValidateConfigDoc checks a structured DEPLOYMENT without writing it: the document
// and the rules, each as the service renders them.
//
// rules is json.RawMessage rather than a typed slice so that ABSENT survives the round
// trip: a rules argument nobody passed must not arrive as an empty array, because the
// service reads absent as "leave the rules alone" and [] as "delete them all". A typed
// parameter cannot express that difference, and getting it wrong deletes every rule
// the operator has.
func (a *App) ValidateConfigDoc(doc map[string]any, rules json.RawMessage) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return a.client.ValidateConfigDoc(a.ctx, b, rules)
}

// SaveConfig validates, writes and applies a STRUCTURED deployment — the primary path.
// The daemon re-serialises the structure to YAML and owns the files.
func (a *App) SaveConfig(doc map[string]any, rules json.RawMessage) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return a.client.SetConfigDoc(a.ctx, b, rules)
}

// ---------------------------------------------------------------------------
// First run.
//
// Everything above this line forwards to the control socket, and that is the right
// shape for a service that is ALREADY RUNNING. It is the wrong shape for a machine
// that has never been set up: the service refuses to start without a valid
// deployment, so there is no socket to forward to, and the one thing a new user
// needs — a configuration — is the one thing the window cannot yet produce.
//
// So first run is a distinct mode with its own storage, and it deliberately does NOT
// go through the service:
//
//   - it writes a USER-OWNED deployment, so this unprivileged window needs no root;
//   - it therefore cannot be the thing that makes the service's config valid, which
//     is why -config-source exists: the service materializes that user deployment
//     into the root-owned path it reads.
//
// The alternative — a privileged helper so the window could write /etc directly —
// was rejected: it moves the only action a new user takes behind a privilege prompt
// at the exact moment they are least likely to understand what is being asked.

// UserConfigDir is where the window writes a first deployment: $XDG_CONFIG_HOME/
// lotsman, falling back to ~/.config/lotsman.
//
// It is the SOURCE side of the source/target pair, not a place the service reads
// from directly — one reader, one place.
func UserConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "lotsman"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("first run needs a home directory: %w", err)
	}
	return filepath.Join(home, ".config", "lotsman"), nil
}

// FirstRunState is what the window needs to decide which screen to show.
type FirstRunState struct {
	// Configured is whether a deployment exists in the user config dir. It is NOT
	// whether the service is reachable: a machine with a configuration and a dead
	// service is a BROKEN machine, and showing first run to it would invite the user
	// to overwrite a working configuration with a fresh one.
	Configured bool `json:"configured"`
	// SourcePath is the deployment directory the window would write.
	SourcePath string `json:"source_path"`
	// ServiceReachable is whether /status answered. False during first run by
	// construction, and also true when the service merely crashed.
	ServiceReachable bool `json:"service_reachable"`
	// Legacy is that the deployment found is a v1 file — configured, but not by this
	// build's schema. The screen says so instead of pretending, because the next
	// thing that happens is a migration and the operator should know which.
	Legacy bool `json:"legacy"`
}

// SetupState is safe to call before any service exists — that is its whole purpose.
//
// It asks the SERVICE first, and only falls back to this window's own default
// directory. The order matters and getting it backwards is a real defect, not a
// style choice: the service reads a root-owned deployment under /etc while the
// window writes ~/.config, so a window that answered only from its own directory
// reported FIRST RUN on a machine running a perfectly good deployment — and then
// offered to overwrite it. That is how a config editor destroys a config.
func (a *App) SetupState() (FirstRunState, error) {
	dir, err := UserConfigDir()
	if err != nil {
		return FirstRunState{}, err
	}
	st := FirstRunState{SourcePath: dir}

	rep, err := a.client.Status(a.ctx)
	if err == nil {
		st.ServiceReachable = true
		// The service knows which deployment it is actually running. Trust that over
		// any guess this window could make about where it ought to be.
		if d := rep.Deployment; d != nil {
			if d.Source != "" {
				st.SourcePath = d.Source
				st.Configured = d.SourceConfigured
			} else {
				st.Configured = d.SourceConfigured
			}
			return st, nil
		}
	}
	// The service could not say. That is either no service at all (the genuine
	// first-run case) or a service OLDER than this window, which predates the
	// deployment field — and it must not read as "the machine is empty" in either case.
	//
	// The first version of this fallback checked only the window's OWN default
	// directory and reported a configured machine as fresh, because the deployment on
	// a real install lives in /etc. So it checks every root this project has ever
	// documented: the window's own, the canonical v2 path, and the legacy v1 name
	// that this machine and older installs still use.
	for _, d := range candidateDeploymentDirs(dir) {
		if _, err := os.Stat(filepath.Join(d, config.DocName)); err == nil {
			st.Configured = true
			st.SourcePath = d
			return st, nil
		}
		// A v1 deployment is still a deployment. It names configuration differently,
		// and calling that machine unconfigured is how an upgrade starts by offering
		// to overwrite the one config the operator has.
		if _, err := os.Stat(filepath.Join(d, "client.yaml")); err == nil {
			st.Configured = true
			st.SourcePath = d
			st.Legacy = true
			return st, nil
		}
	}
	return st, nil
}

// candidateDeploymentDirs is the ordered list of roots to look in when the service
// cannot say where it reads. Ordered by how likely each is to be the real one.
func candidateDeploymentDirs(own string) []string {
	out := []string{own}
	for _, d := range []string{"/etc/lotsman", "/var/lib/lotsman"} {
		if d != own {
			out = append(out, d)
		}
	}
	return out
}

// ScaffoldChoice is which starting deployment to write.
type ScaffoldChoice string

const (
	ScaffoldRecommended ScaffoldChoice = "recommended"
	ScaffoldMinimal     ScaffoldChoice = "minimal"
)

// WriteFirstDeployment writes a starter deployment to the user config dir, WITHOUT
// a running service and WITHOUT root.
//
// It refuses to overwrite: this is the one operation in the window that can destroy
// work, and a user who clicks it twice, or whose window re-renders into a second
// call, must not lose a configuration they have since edited by hand.
func (a *App) WriteFirstDeployment(choice ScaffoldChoice, subURLs []string) (string, error) {
	dir, err := UserConfigDir()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, config.DocName)); err == nil {
		return "", fmt.Errorf(
			"a configuration already exists at %s — refusing to overwrite it. Edit it in Config instead, "+
				"or move it aside and start over", dir)
	}

	var dep scaffold.Deployment
	switch choice {
	case ScaffoldRecommended:
		dep = scaffold.Recommended(subURLs)
	case ScaffoldMinimal:
		if len(subURLs) == 0 {
			// The service refuses to start with no nodes, so a minimal deployment with
			// no subscription produces a configuration that is syntactically fine and
			// practically dead. Saying so here is cheaper than the user finding out
			// from a service that will not start.
			return "", fmt.Errorf("the minimal deployment needs at least one subscription URL")
		}
		dep = scaffold.Starter(subURLs)
	default:
		return "", fmt.Errorf("unknown starting set %q (want %q or %q)", choice, ScaffoldRecommended, ScaffoldMinimal)
	}

	// Validate through the loader the service will use, before anything is written.
	// A starter that does not load is the worst outcome here: the user would have a
	// config file, a service that refuses it, and no way to tell which is at fault.
	scratch, err := os.MkdirTemp("", "lotsman-firstrun-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	if err := writeDeployment(scratch, dep); err != nil {
		return "", err
	}
	if _, err := config.LoadV2(scratch, "", ""); err != nil {
		return "", fmt.Errorf("the generated deployment does not load, refusing to write it: %w", err)
	}

	if err := writeDeployment(dir, dep); err != nil {
		return "", err
	}
	return dir, nil
}

// writeDeployment puts a Deployment on disk: the document, then one file per rule.
func writeDeployment(dir string, dep scaffold.Deployment) error {
	if err := os.MkdirAll(filepath.Join(dir, config.RulesDirName), 0o700); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(dir, config.DocName), dep.Document, 0o600); err != nil {
		return err
	}
	names := make([]string, 0, len(dep.Rules))
	for name := range dep.Rules {
		names = append(names, name)
	}
	sort.Strings(names) // stable order, so a repeated write is byte-identical
	for _, name := range names {
		if err := atomicfile.Write(filepath.Join(dir, config.RulesDirName, name), dep.Rules[name], 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ServiceUnit describes how the service on THIS machine is started, as far as the
// window can find out without guessing.
//
// It exists because the first-run screen used to print
// `systemctl --user restart lotsman`, which is wrong twice over: the unit is a SYSTEM
// unit, and on this machine it is called `lotsman-client.service`. A command printed
// by the app that cannot work is worse than no command — it sends the operator to a
// shell to be told "no such unit" at exactly the moment they have just finished
// setup and are least inclined to debug.
//
// So the window asks systemd. `list-unit-files` is read-only and needs no privilege,
// and if nothing is installed it says so rather than inventing a name.
type ServiceUnit struct {
	// Installed is whether a lotsman systemd unit was found at all.
	Installed bool `json:"installed"`
	// Name is the unit's exact name, e.g. "lotsman-client.service".
	Name string `json:"name"`
	// Scope is "system" or "user" — it decides the --user flag, and guessing it wrong
	// produces a command that silently does nothing.
	Scope string `json:"scope"`
	// Running is whether it is active right now.
	Running bool `json:"running"`
}

// ServiceUnitInfo looks the unit up. It returns Installed:false rather than an error
// when there is none: "no unit on this machine" is a fact the screen shows, not a
// failure.
func (a *App) ServiceUnitInfo() ServiceUnit {
	type scopeSpec struct {
		scope string
		args  []string
	}
	specs := []scopeSpec{
		{"system", []string{"list-unit-files", "--type=service", "--no-legend", "--no-pager"}},
		{"user", []string{"--user", "list-unit-files", "--type=service", "--no-legend", "--no-pager"}},
	}
	for _, sp := range specs {
		out, err := exec.Command("systemctl", sp.args...).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name := fields[0]
			if !strings.HasSuffix(name, ".service") || !strings.Contains(name, "lotsman") {
				continue
			}
			// An ENABLED unit is the installed one; a disabled one left over from a
			// previous generation is not what starts this machine.
			if fields[1] != "enabled" {
				continue
			}
			u := ServiceUnit{Installed: true, Name: name, Scope: sp.scope}
			args := []string{"is-active"}
			if sp.scope == "user" {
				args = append([]string{"--user"}, args...)
			}
			args = append(args, name)
			if b, err := exec.Command("systemctl", args...).Output(); err == nil {
				u.Running = strings.TrimSpace(string(b)) == "active"
			}
			return u
		}
	}
	return ServiceUnit{}
}
