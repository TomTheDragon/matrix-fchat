package connector

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"matrix-fchat/pkg/fchat"
)

func TestContactEntriesAndRender(t *testing.T) {
	fc := &FChatConnector{Bridge: &bridgev2.Bridge{Config: &bridgeconfig.BridgeConfig{CommandPrefix: "!fchat"}}}
	c := &FChatClient{
		Main: fc,
		characters: map[networkid.UserID]*characterInfo{
			"zed": {Name: "Zed", Gender: "Female", Status: fchat.StatusLooking, StatusMsg: "[b]LF[/b] &lt;RP&gt;"},
		},
	}
	list := map[networkid.UserID]string{"alice": "Alice", "zed": "Zed", "bob": "Bob"}

	entries := c.contactEntries(list, "")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "Zed,Alice,Bob" {
		t.Fatalf("expected online characters first, then alphabetical, got %v", names)
	}
	if got := c.contactEntries(list, "ali"); len(got) != 1 || got[0].Name != "Alice" {
		t.Fatalf("filter didn't work: %v", got)
	}

	entries[0].AvatarMXC = "mxc://example.com/zed"
	entries[0].ChatLink = "https://matrix.to/#/%23fchat_open_x:example.com"
	content := fc.renderContactList("Your bookmarks", entries, 5, 1, "!fchat bookmarks * 5")
	for _, want := range []string{
		"Your bookmarks (showing 3 of 5, 1 online):",
		`<img src="mxc://example.com/zed" alt="" title="Zed" width="24" height="24">`,
		`<a href="https://matrix.to/#/%23fchat_open_x:example.com"><font color="#ff6699" data-mx-color="#ff6699"><strong>Zed</strong></font></a>`,
		`<code><a href="https://www.f-list.net/c/Zed" title="F-List profile">🏠</a></code>`,
		"🟢 looking: LF &lt;RP&gt;",
		"⚫ offline",
		"<code>!fchat start-chat &lt;name&gt;</code>",
		"<code>!fchat bookmarks * 5</code>",
	} {
		if !strings.Contains(content.FormattedBody, want) {
			t.Errorf("HTML doesn't contain %q:\n%s", want, content.FormattedBody)
		}
	}
	if !strings.Contains(content.Body, "* Zed – 🟢 looking: LF <RP>") {
		t.Errorf("unexpected plain body:\n%s", content.Body)
	}
}

func TestParseListArgs(t *testing.T) {
	tests := []struct {
		args   []string
		filter string
		count  int
	}{
		{nil, "", 10},
		{[]string{"Ali"}, "ali", 10},
		{[]string{"42"}, "42", 10},
		{[]string{"*", "25"}, "", 25},
		{[]string{"some", "name", "3"}, "some name", 3},
		{[]string{"*", "100000"}, "", maxListedContacts},
		{[]string{"*"}, "", 10},
	}
	for _, tt := range tests {
		filter, count, err := parseListArgs(tt.args)
		if err != nil || filter != tt.filter || count != tt.count {
			t.Errorf("parseListArgs(%q) = %q, %d, %v; want %q, %d", tt.args, filter, count, err, tt.filter, tt.count)
		}
	}
	if _, _, err := parseListArgs([]string{"*", "0"}); err == nil {
		t.Error("expected an error for 0 entries")
	}
}
