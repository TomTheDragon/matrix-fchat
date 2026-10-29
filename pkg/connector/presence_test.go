package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/fchat"
)

func TestPresenceRequest(t *testing.T) {
	tests := []struct {
		status   fchat.Status
		msg      string
		presence event.Presence
		wantMsg  string
	}{
		{fchat.StatusOnline, "hi", event.PresenceOnline, "hi"},
		{fchat.StatusLooking, "", event.PresenceOnline, "Looking"},
		{fchat.StatusBusy, "brb", event.PresenceUnavailable, "Busy: brb"},
		{fchat.StatusOffline, "ignored", event.PresenceOffline, ""},
		{"", "", event.PresenceOffline, ""},
	}
	for _, tt := range tests {
		req := presenceRequest(tt.status, tt.msg)
		if req.Presence != tt.presence || req.StatusMsg != tt.wantMsg {
			t.Errorf("presenceRequest(%q, %q) = %q/%q, want %q/%q", tt.status, tt.msg, req.Presence, req.StatusMsg, tt.presence, tt.wantMsg)
		}
	}
}
