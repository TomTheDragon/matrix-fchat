package connector

import (
	"strings"
	"testing"
	"time"

	"matrix-fchat/pkg/fchat"
)

func TestFindFriendRequest(t *testing.T) {
	incoming := []fchat.FriendRequest{
		{ID: 1, Source: "Them", Dest: "Alt"},
		{ID: 2, Source: "Them", Dest: "Me"},
		{ID: 3, Source: "Other", Dest: "Me"},
	}
	if id := findFriendRequest(incoming, "me", "them", true); id != 2 {
		t.Errorf("expected the request to the own character, got %d", id)
	}
	if id := findFriendRequest(incoming, "Third", "Them", true); id != 1 {
		t.Errorf("expected the request to another character as fallback, got %d", id)
	}
	if id := findFriendRequest(incoming, "Me", "Nobody", true); id != 0 {
		t.Errorf("expected no request, got %d", id)
	}
	outgoing := []fchat.FriendRequest{{ID: 4, Source: "Me", Dest: "Them"}}
	if id := findFriendRequest(outgoing, "Me", "Them", false); id != 4 {
		t.Errorf("expected outgoing request, got %d", id)
	}
}

func TestFormatFriendRequests(t *testing.T) {
	out := formatFriendRequests(&fchat.FriendRequests{
		Incoming: []fchat.FriendRequest{{Source: "Zed", Dest: "Me"}, {Source: "Amy", Dest: "Alt"}},
		Outgoing: []fchat.FriendRequest{{Source: "Me", Dest: "Bob"}},
	}, "Me")
	for _, want := range []string{"Incoming friend requests** (2)", "* **Amy** (to/from your character Alt)\n* **Zed**\n",
		"Sent friend requests** (1)", "* **Bob**", "friend accept <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if got := formatFriendRequests(&fchat.FriendRequests{}, "Me"); got != "No open friend requests" {
		t.Errorf("unexpected empty output %q", got)
	}
}

func TestParseMemoArgs(t *testing.T) {
	for _, tc := range []struct {
		raw, name, text string
		set             bool
	}{
		{"", "", "", false},
		{"Some Name", "Some Name", "", false},
		{"Some Name = likes tea = lots", "Some Name", "likes tea = lots", true},
		{"Some Name =", "Some Name", "", true},
		{"= in a DM", "", "in a DM", true},
	} {
		name, set, text := parseMemoArgs(tc.raw)
		if name != tc.name || set != tc.set || text != tc.text {
			t.Errorf("parseMemoArgs(%q) = %q, %v, %q", tc.raw, name, set, text)
		}
	}
}

func TestCompareKinks(t *testing.T) {
	own := map[string]string{"1": "fave", "2": "yes", "3": "no", "4": "fave", "5": "maybe"}
	theirs := map[string]string{"1": "fave", "2": "fave", "3": "yes", "4": "no", "5": "no", "6": "fave"}
	match := compareKinks(own, theirs, map[string]string{"1": "Biting"})
	if match.Shared != 2 || match.Conflicts != 2 || len(match.BothFavorites) != 1 || match.BothFavorites[0] != "Biting" {
		t.Errorf("unexpected match %+v", match)
	}
	match.Own = "Me"
	if got := match.summary(); got != "Kink match with Me: 50% (2 liked by both, 2 conflicts)" {
		t.Errorf("unexpected summary %q", got)
	}
}

func TestFormatUptime(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	out := formatUptime(&fchat.Uptime{StartTime: start.Unix(), Users: 5000, MaxUsers: 9000, Channels: 300, Accepted: 123},
		start.Add(50*time.Hour+30*time.Minute))
	for _, want := range []string{"online: 5000 (record: 9000)", "Channels: 300", "2026-10-01 12:00 UTC (2 days, 2 hours)", "accepted since then: 123"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestAutoAdTimes(t *testing.T) {
	ad := &autoAd{Interval: 15, Started: 1000, LastPosted: 1600}
	if got := ad.next().Unix(); got != 1600+15*60 {
		t.Errorf("next = %d", got)
	}
	if got := ad.expires().Unix(); got != 1000+3*3600 {
		t.Errorf("expires = %d", got)
	}
}
