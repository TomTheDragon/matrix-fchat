package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
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

func TestGhostCharacterName(t *testing.T) {
	fc := &FChatConnector{Config: Config{OfflineNameSuffix: " (offline)"}}
	ghost := func(name, suffix string) *bridgev2.Ghost {
		return &bridgev2.Ghost{Ghost: &database.Ghost{Name: name, Metadata: &GhostMetadata{NameSuffix: suffix}}}
	}
	tests := []struct {
		ghost *bridgev2.Ghost
		want  string
	}{
		{ghost("Some Name (offline)", " (offline)"), "Some Name"},
		{ghost("Some Name [away]", " [away]"), "Some Name"},
		{ghost("Some Name (offline)", ""), "Some Name"},
		{ghost("Some Name", ""), "Some Name"},
		{ghost("", ""), ""},
	}
	for _, tt := range tests {
		if got := fc.ghostCharacterName(tt.ghost); got != tt.want {
			t.Errorf("ghostCharacterName(%q) = %q, want %q", tt.ghost.Name, got, tt.want)
		}
	}
}
