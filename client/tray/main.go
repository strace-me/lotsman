// Command lotsman-tray is the unprivileged systray companion. It never touches the
// data plane: it polls the service's /status over the control socket, recolours a
// sextant glyph by verdict (neutral / amber / red), and offers Open (launch the
// GUI), Stop (master off), and Quit-the-tray. The privilege boundary is the socket
// — this process links only client/control, never the engine.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"fyne.io/systray"

	"github.com/strace-me/lotsman/client/control"
)

// Glyphs pre-rendered from client/desktop/assets/tray.svg in three state colours
// (systray takes raster bytes, so state colour cannot be a runtime currentColor).
var (
	//go:embed icon_neutral.png
	iconNeutral []byte
	//go:embed icon_amber.png
	iconAmber []byte
	//go:embed icon_red.png
	iconRed []byte
)

var (
	socketPath = flag.String("socket", "", "control socket path (empty = default under the runtime dir)")
	guiBin     = flag.String("gui", "lotsman-gui", "GUI binary launched by Open (name on PATH or a full path)")
	interval   = flag.Duration("interval", 3*time.Second, "how often to poll /status")
)

func main() {
	flag.Parse()
	if *socketPath == "" {
		*socketPath = control.DefaultSocketPath()
	}
	t := &tray{client: control.New(*socketPath)}
	systray.Run(t.onReady, func() {})
}

type tray struct {
	client *control.Client
	status *systray.MenuItem
}

func (t *tray) onReady() {
	systray.SetIcon(iconNeutral)
	systray.SetTitle("")
	systray.SetTooltip("Lotsman")

	t.status = systray.AddMenuItem("…", "")
	t.status.Disable()
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("Открыть Lotsman", "Открыть окно приложения")
	mStop := systray.AddMenuItem("Выключить сервис", "Мастер-выключатель — остановить сервис")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выход из трея", "Закрыть только иконку в трее")

	go t.poll()

	for {
		select {
		case <-mOpen.ClickedCh:
			t.launchGUI()
		case <-mStop.ClickedCh:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := t.client.Stop(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "tray: stop:", err)
			}
			cancel()
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (t *tray) poll() {
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	t.refresh()
	for range tick.C {
		t.refresh()
	}
}

// refresh pulls /status once and reflects it in the glyph, tooltip and the
// disabled status line. A dead socket is the red "service unreachable" state.
func (t *tray) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	rep, err := t.client.Status(ctx)
	if err != nil {
		systray.SetIcon(iconRed)
		systray.SetTooltip("Lotsman — сервис недоступен")
		t.status.SetTitle("сервис недоступен")
		return
	}
	switch {
	case !rep.Running || rep.Verdict.State == "down":
		systray.SetIcon(iconRed)
	case rep.Verdict.State == "working":
		systray.SetIcon(iconNeutral)
	default: // partial | not-working
		systray.SetIcon(iconAmber)
	}
	line := statusLine(rep)
	systray.SetTooltip("Lotsman — " + line)
	t.status.SetTitle(line)
}

// statusLine is the human summary, e.g. "работает · 8/10 · пул 150".
func statusLine(r control.Report) string {
	head := map[string]string{
		"working":     "работает",
		"partial":     "не все сервисы",
		"not-working": "не работает",
		"down":        "выключено",
	}[r.Verdict.State]
	if head == "" {
		head = r.Verdict.State
	}
	ok := r.Verdict.Total - r.Verdict.Broken - r.Verdict.Failing
	s := fmt.Sprintf("%s · %d/%d", head, ok, r.Verdict.Total)
	if r.Fleet.Total > 0 {
		s += fmt.Sprintf(" · пул %d", r.Fleet.Total)
	}
	return s
}

func (t *tray) launchGUI() {
	cmd := exec.Command(*guiBin)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "tray: launch GUI:", err)
	}
}
