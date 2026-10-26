package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestDisableLinkPreviewsSerialization(t *testing.T) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "x"}
	disableLinkPreviews(content)
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"com.beeper.linkpreviews":[]`) {
		t.Errorf("empty preview list missing: %s", data)
	}
}

func TestIsFListURL(t *testing.T) {
	c := &FChatClient{Main: &FChatConnector{Config: Config{SiteURL: "https://test.example.org"}}}
	for href, want := range map[string]bool{
		"https://www.f-list.net/c/x":        true,
		"https://static.f-list.net/a.png":   true,
		"https://f-list.net":                true,
		"https://test.example.org/c/x":      true,
		"https://notf-list.net/":            false,
		"https://example.com/f-list.net":    false,
		"https://matrix.to/#/%23fchat_open": false,
	} {
		if got := c.isFListURL(href); got != want {
			t.Errorf("isFListURL(%q) = %v, want %v", href, got, want)
		}
	}
}
