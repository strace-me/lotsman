package stunprobe

import (
	"encoding/binary"
	"testing"
)

func TestBuildBindingRequest(t *testing.T) {
	tx := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	msg := BuildBindingRequest(tx)
	if len(msg) != headerLen {
		t.Fatalf("len = %d, want %d", len(msg), headerLen)
	}
	if binary.BigEndian.Uint16(msg[0:2]) != bindingRequest {
		t.Errorf("type = %x, want binding request", msg[0:2])
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie {
		t.Errorf("magic cookie wrong")
	}
	// A request must NOT validate as a response to itself (class bit unset).
	if IsBindingResponse(msg, tx) {
		t.Error("a Binding Request must not be accepted as a response")
	}
}

func TestIsBindingResponse(t *testing.T) {
	tx := [12]byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 0xAA, 0xBB}

	// craft a valid Binding Success Response (class bit set).
	good := make([]byte, headerLen)
	binary.BigEndian.PutUint16(good[0:2], 0x0101) // Binding success response
	binary.BigEndian.PutUint32(good[4:8], magicCookie)
	copy(good[8:20], tx[:])
	if !IsBindingResponse(good, tx) {
		t.Error("valid binding response rejected")
	}

	// wrong transaction ID -> reject (this is how corruption/crosstalk shows up).
	other := [12]byte{1}
	if IsBindingResponse(good, other) {
		t.Error("response with mismatched transaction ID must be rejected")
	}

	// wrong magic cookie -> reject (corrupted header).
	badCookie := append([]byte(nil), good...)
	badCookie[4] = 0
	if IsBindingResponse(badCookie, tx) {
		t.Error("response with wrong magic cookie must be rejected")
	}

	// too short -> reject.
	if IsBindingResponse(good[:10], tx) {
		t.Error("truncated response must be rejected")
	}
}
