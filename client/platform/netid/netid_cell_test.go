package netid

import "testing"

func TestIsCellularIface(t *testing.T) {
	cell := []string{"rmnet0", "wwan0", "wwp0s20f0u6", "ppp0", "cdc-wdm0"}
	for _, i := range cell {
		if !isCellularIface(i) {
			t.Errorf("%q should be detected as cellular", i)
		}
	}
	// Wi-Fi, ethernet, and USB/RNDIS phone-tether (which NATs and has a real MAC)
	// must NOT be treated as a direct modem.
	notCell := []string{"wlp0s20f3", "eth0", "enp3s0", "usb0", "rndis0", "tun0"}
	for _, i := range notCell {
		if isCellularIface(i) {
			t.Errorf("%q must NOT be treated as cellular", i)
		}
	}
}

func TestParseFirstModem(t *testing.T) {
	out := "    /org/freedesktop/ModemManager1/Modem/2 [Quectel] EG25-G\n"
	if got := parseFirstModem(out); got != "2" {
		t.Errorf("parseFirstModem = %q, want 2", got)
	}
	if got := parseFirstModem("No modems were found\n"); got != "" {
		t.Errorf("no modem should yield empty, got %q", got)
	}
}

func TestParseOperatorCode(t *testing.T) {
	out := "modem.3gpp.registration-state : home\nmodem.3gpp.operator-code     : 25001\nmodem.3gpp.operator-name     : MTS RUS\n"
	if got := parseOperatorCode(out); got != "25001" {
		t.Errorf("parseOperatorCode = %q, want 25001", got)
	}
	// Not registered: operator-code is "--".
	if got := parseOperatorCode("modem.3gpp.operator-code : --\n"); got != "" {
		t.Errorf("unregistered modem should yield empty, got %q", got)
	}
}
