package connector

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
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

func TestStickerInfoSerialization(t *testing.T) {
	info := &event.FileInfo{MimeType: "image/gif", Width: 100, Height: 100, IsAnimated: true,
		Extra: map[string]any{"org.matrix.msc4230.is_animated": true}}
	data, err := json.Marshal(&event.MessageEventContent{Body: "x", URL: "mxc://a/b", Info: info})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"org.matrix.msc4230.is_animated":true`, `"is_animated":true`, `"mimetype":"image/gif"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s missing in %s", want, data)
		}
	}
}

func TestDescribeImage(t *testing.T) {
	var buf bytes.Buffer
	pal := color.Palette{color.Black, color.White}
	anim := &gif.GIF{
		Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 3), pal), image.NewPaletted(image.Rect(0, 0, 4, 3), pal)},
		Delay: []int{10, 10},
	}
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	img := describeImage(buf.Bytes())
	if img.MimeType != "image/gif" || img.Width != 4 || img.Height != 3 || !img.Animated {
		t.Errorf("unexpected image info %+v", img)
	}
}
