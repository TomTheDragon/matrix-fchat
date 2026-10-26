package connector

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
)

func TestHTMLToBBCode(t *testing.T) {
	c := &FChatClient{Main: &FChatConnector{Bridge: &bridgev2.Bridge{}}}
	tests := []struct{ in, out string }{
		{"<strong>bold</strong> and <em>italic</em>", "[b]bold[/b] and [i]italic[/i]"},
		{"<u>u</u><del>s</del>", "[u]u[/u][s]s[/s]"},
		{`<a href="https://example.com">site</a>`, "[url=https://example.com]site[/url]"},
		{`<a href="https://example.com">https://example.com</a>`, "[url]https://example.com[/url]"},
		{`<span data-mx-spoiler>secret</span>`, "[spoiler]secret[/spoiler]"},
		{`<font color="#ff0000">red</font>`, "[color=red]red[/color]"},
		{`<font color="#123456">odd</font>`, "odd"},
		{"<code>[b]x[/b]</code>", "[noparse][b]x[/b][/noparse]"},
		{"line1<br>line2", "line1\nline2"},
	}
	for _, tt := range tests {
		got := c.htmlToBBCode(context.Background(), tt.in)
		if got != tt.out {
			t.Errorf("htmlToBBCode(%q):\n got %q\nwant %q", tt.in, got, tt.out)
		}
	}
}

func TestPortalIDs(t *testing.T) {
	key := makeChannelPortalKey("ADH-abc", "me")
	channel, dm, ok := parsePortalID(key.ID)
	if !ok || channel != "ADH-abc" || dm != "" {
		t.Errorf("unexpected channel parse result %q %q %v", channel, dm, ok)
	}
	key = makeDMPortalKey("Some Name", "me")
	channel, dm, ok = parsePortalID(key.ID)
	if !ok || channel != "" || dm != "some name" {
		t.Errorf("unexpected DM parse result %q %q %v", channel, dm, ok)
	}
	if !isValidCharacterName("Some Name-2_x") || isValidCharacterName(" bad") || isValidCharacterName("a<b") {
		t.Error("character name validation is wrong")
	}
}

func TestMemberSyncLimit(t *testing.T) {
	cfg := Config{MemberSyncLimit: 10}
	if !cfg.ShouldSyncMembers(10) || cfg.ShouldSyncMembers(11) {
		t.Error("limit not applied")
	}
	cfg.MemberSyncLimit = 0
	if !cfg.ShouldSyncMembers(100000) {
		t.Error("0 should mean unlimited")
	}
	cfg.MemberSyncLimit = -1
	if cfg.ShouldSyncMembers(1) {
		t.Error("-1 should disable syncing")
	}
}

func TestAvatarIDRefresh(t *testing.T) {
	const url = "https://static.f-list.net/images/avatar/x.png"
	start := time.Unix(0, 0).Add(10 * avatarRefreshPeriod)
	if avatarID(url, start) != avatarID(url, start.Add(avatarRefreshPeriod-time.Second)) {
		t.Error("avatar ID changed within one refresh period")
	}
	if avatarID(url, start) == avatarID(url, start.Add(avatarRefreshPeriod)) {
		t.Error("avatar ID didn't change after a refresh period")
	}
}
