package remctl

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/singbox"
)

func TestHotActionsApplyRejectQUIC(t *testing.T) {
	files := map[string]string{}
	h := HotActions{
		Dir:         "/d",
		RejectMatch: func(s string) ([]string, []string) { return []string{"googlevideo.com"}, []string{"142.250.0.0/15"} },
		Write:       func(p string, b []byte) error { files[p] = string(b); return nil },
	}
	a := h.Actions()
	if err := a.Apply("youtube", 2, singbox.Remediation{RejectQUIC: true}); err != nil {
		t.Fatal(err)
	}
	rq := files[singbox.RemFilePath("/d", "rem-rq-youtube")]
	if !strings.Contains(rq, "googlevideo.com") || !strings.Contains(rq, "142.250.0.0/15") {
		t.Errorf("reject file should carry the service match: %s", rq)
	}
	// fallback file written but empty (no fallback in this rem)
	fb := files[singbox.RemFilePath("/d", "rem-fb-youtube")]
	if !strings.Contains(fb, `"rules": []`) {
		t.Errorf("fallback file should be empty/disarmed: %s", fb)
	}
}

func TestHotActionsApplyFallback(t *testing.T) {
	files := map[string]string{}
	h := HotActions{Dir: "/d", Write: func(p string, b []byte) error { files[p] = string(b); return nil }}
	if err := h.Actions().Apply("youtube", 1, singbox.Remediation{FallbackCIDRs: []string{"142.250.0.0/15"}}); err != nil {
		t.Fatal(err)
	}
	fb := files[singbox.RemFilePath("/d", "rem-fb-youtube")]
	if !strings.Contains(fb, "142.250.0.0/15") {
		t.Errorf("fallback file should carry cidrs: %s", fb)
	}
	// reject file written empty (reject not active)
	if rq := files[singbox.RemFilePath("/d", "rem-rq-youtube")]; !strings.Contains(rq, `"rules": []`) {
		t.Errorf("reject file should be disarmed: %s", rq)
	}
}

func TestHotActionsRollbackDisarmsBoth(t *testing.T) {
	files := map[string]string{}
	h := HotActions{Dir: "/d", Write: func(p string, b []byte) error { files[p] = string(b); return nil }}
	if err := h.Actions().Rollback("youtube"); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"rem-rq-youtube", "rem-fb-youtube"} {
		if !strings.Contains(files[singbox.RemFilePath("/d", tag)], `"rules": []`) {
			t.Errorf("%s must be disarmed after rollback", tag)
		}
	}
}
