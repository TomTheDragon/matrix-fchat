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
			"zed": {Name: "Zed", Status: fchat.StatusLooking, StatusMsg: "[b]LF[/b] &lt;RP&gt;"},
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
	content := fc.renderContactList("Your bookmarks", entries, 5)
	for _, want := range []string{
		"Your bookmarks (showing 3 of 5, 1 online):",
		`<img src="mxc://example.com/zed" alt="" title="Zed" width="24" height="24">`,
		"🟢 looking: LF &lt;RP&gt;",
		"⚫ offline",
		"<code>!fchat start-chat &lt;name&gt;</code>",
	} {
		if !strings.Contains(content.FormattedBody, want) {
			t.Errorf("HTML doesn't contain %q:\n%s", want, content.FormattedBody)
		}
	}
	if !strings.Contains(content.Body, "* Zed – 🟢 looking: LF <RP>") {
		t.Errorf("unexpected plain body:\n%s", content.Body)
	}
}
