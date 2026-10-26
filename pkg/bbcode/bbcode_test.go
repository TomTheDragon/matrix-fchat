package bbcode

import (
	"strings"
	"testing"
)

var testOpts = Options{
	ProfileURL: func(name string) string { return "https://www.f-list.net/c/" + name },
	EIconURL:   func(name string) string { return "https://static.f-list.net/images/eicon/" + name + ".gif" },
}

func TestToHTML(t *testing.T) {
	tests := []struct {
		name, in, html, plain string
	}{
		{"plain", "hello world", "hello world", "hello world"},
		{"escape", "a < b & c", "a &lt; b &amp; c", "a < b & c"},
		{"newline", "a\nb", "a<br>b", "a\nb"},
		{"bold", "[b]bold[/b]", "<strong>bold</strong>", "bold"},
		{"nested", "[b][i]x[/i][/b]", "<strong><em>x</em></strong>", "x"},
		{"case insensitive", "[B]x[/B]", "<strong>x</strong>", "x"},
		{"unclosed", "[b]x", "<strong>x</strong>", "x"},
		{"stray close", "x[/b]", "x[/b]", "x[/b]"},
		{"unknown tag", "[foo]x[/foo]", "[foo]x[/foo]", "[foo]x[/foo]"},
		{"misnested", "[b]a[i]b[/b]c[/i]", "<strong>a<em>b</em></strong>c[/i]", "abc[/i]"},
		{"color", "[color=red]x[/color]", `<font color="#ff0000" data-mx-color="#ff0000">x</font>`, "x"},
		{"bad color", "[color=javascript]x[/color]", "x", "x"},
		{"url arg", "[url=https://example.com]site[/url]", `<a href="https://example.com">site</a>`, "site (https://example.com)"},
		{"url plain", "[url]https://example.com[/url]", `<a href="https://example.com">https://example.com</a>`, "https://example.com"},
		{"url unsafe", "[url=javascript:alert(1)]x[/url]", "x", "x"},
		{"url attr escape", `[url=https://e.com/"><script>]x[/url]`, `<a href="https://e.com/&#34;&gt;&lt;script&gt;">x</a>`, `x (https://e.com/"><script>)`},
		{"user", "[user]Some Name[/user]", `<code><a href="https://www.f-list.net/c/Some Name">Some Name</a></code>`, "Some Name"},
		{"noparse", "[noparse][b]x[/b][/noparse]", "[b]x[/b]", "[b]x[/b]"},
		{"eicon", "[eicon]wave[/eicon]", `<a href="https://static.f-list.net/images/eicon/wave.gif">:wave:</a>`, ":wave:"},
		{"spoiler", "[spoiler]x[/spoiler]", "<span data-mx-spoiler>x</span>", "||x||"},
		{"session", "[session=My Room]ADH-123[/session]", "<strong>#My Room</strong> (<code>ADH-123</code>)", "#My Room (ADH-123)"},
		{"bracket text", "[not a tag", "[not a tag", "[not a tag"},
		{"tag with newline", "[b\n]x", "[b<br>]x", "[b\n]x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ToHTML(tt.in, testOpts)
			if res.HTML != tt.html {
				t.Errorf("HTML:\n got %q\nwant %q", res.HTML, tt.html)
			}
			if res.Plain != tt.plain {
				t.Errorf("Plain:\n got %q\nwant %q", res.Plain, tt.plain)
			}
		})
	}
}

func TestUserMention(t *testing.T) {
	opts := testOpts
	opts.UserMention = func(name string) string { return "@fchat_" + name + ":example.com" }
	res := ToHTML("hi [user]bob[/user] [icon]bob[/icon]", opts)
	if len(res.Mentions) != 2 || res.Mentions[0] != "@fchat_bob:example.com" {
		t.Errorf("unexpected mentions %v", res.Mentions)
	}
	// Mentions are reported, but not rendered as pills (Element would hide the name).
	if strings.Contains(res.HTML, "matrix.to") {
		t.Errorf("mention rendered as pill: %s", res.HTML)
	}
}

func TestImagesAndLinks(t *testing.T) {
	opts := testOpts
	opts.UserLink = func(name string) (string, string) { return "https://matrix.to/#/%23open_" + name, "#6699ff" }
	opts.AvatarImage = func(name string) string { return "mxc://x/avatar_" + name }
	opts.EIconImage = func(name string) string {
		if name == "missing" {
			return ""
		}
		return "mxc://x/eicon_" + name
	}
	opts.EmoticonImage = func(name string) string { return "mxc://x/" + name }
	opts.NoPreview = func(href string) bool { return strings.Contains(href, "f-list.net") }
	tests := []struct {
		name, in, html, plain string
	}{
		{"user", "[user]bob[/user]",
			`<code><a href="https://www.f-list.net/c/bob"><font color="#6699ff" data-mx-color="#6699ff">bob</font></a>` +
				` <a href="https://matrix.to/#/%23open_bob">💬</a></code>`, "bob"},
		{"icon", "[icon]bob[/icon]",
			`<code><a href="https://www.f-list.net/c/bob"><img src="mxc://x/avatar_bob" alt="bob" title="bob" width="50" height="50"></a>` +
				` <a href="https://matrix.to/#/%23open_bob">💬</a></code>`, "bob"},
		{"icons on one line", "[icon]a[/icon][icon]b[/icon] [icon]c[/icon]",
			`<code><a href="https://www.f-list.net/c/a"><img src="mxc://x/avatar_a" alt="a" title="a" width="50" height="50"></a>` +
				` <a href="https://matrix.to/#/%23open_a">💬</a></code>` +
				`<code><a href="https://www.f-list.net/c/b"><img src="mxc://x/avatar_b" alt="b" title="b" width="50" height="50"></a>` +
				` <a href="https://matrix.to/#/%23open_b">💬</a></code> ` +
				`<code><a href="https://www.f-list.net/c/c"><img src="mxc://x/avatar_c" alt="c" title="c" width="50" height="50"></a>` +
				` <a href="https://matrix.to/#/%23open_c">💬</a></code>`, "ab c"},
		{"eicon", "[eicon]wave[/eicon]",
			`<img src="mxc://x/eicon_wave" alt="wave" title="wave" width="50" height="50">`, ":wave:"},
		{"missing eicon", "[eicon]missing[/eicon]",
			`<code><a href="https://static.f-list.net/images/eicon/missing.gif">:missing:</a></code>`, ":missing:"},
		{"emoticon", "hi :heart: :nope: :lif-smile:x",
			`hi <img data-mx-emoticon src="mxc://x/heart" alt=":heart:" title=":heart:" height="21"> :nope: <img data-mx-emoticon src="mxc://x/lif-smile" alt=":lif-smile:" title=":lif-smile:" height="21">x`,
			"hi :heart: :nope: :lif-smile:x"},
		{"emoticon after colon", "time: 5:heart:", `time: 5<img data-mx-emoticon src="mxc://x/heart" alt=":heart:" title=":heart:" height="21">`, "time: 5:heart:"},
		{"noparse emoticon", "[noparse]:heart:[/noparse]", ":heart:", ":heart:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ToHTML(tt.in, opts)
			if res.HTML != tt.html {
				t.Errorf("HTML:\n got %q\nwant %q", res.HTML, tt.html)
			}
			if res.Plain != tt.plain {
				t.Errorf("Plain:\n got %q\nwant %q", res.Plain, tt.plain)
			}
		})
	}
}

func TestPreviewLinks(t *testing.T) {
	opts := testOpts
	opts.NoPreview = func(href string) bool { return strings.Contains(href, "f-list.net") }
	res := ToHTML("[url]https://www.f-list.net/c/x[/url] [url=https://example.com]e[/url] [user]x[/user]", opts)
	if res.HiddenPreviews != 2 || len(res.PreviewLinks) != 1 || res.PreviewLinks[0] != "https://example.com" {
		t.Errorf("unexpected links: %d hidden, %v", res.HiddenPreviews, res.PreviewLinks)
	}
	if !strings.HasPrefix(res.HTML, `<code><a href="https://www.f-list.net/c/x">https://www.f-list.net/c/x</a></code>`) {
		t.Errorf("F-List link not wrapped in code: %s", res.HTML)
	}
}

func TestOnlyEIcons(t *testing.T) {
	if names, ok := OnlyEIcons(" [eicon]a[/eicon]\n[EICON]b c[/EICON] "); !ok || len(names) != 2 || names[1] != "b c" {
		t.Errorf("unexpected result %v %v", names, ok)
	}
	for _, in := range []string{"hi [eicon]a[/eicon]", "[b][eicon]a[/eicon][/b]", "", "[eicon][/eicon]"} {
		if _, ok := OnlyEIcons(in); ok {
			t.Errorf("OnlyEIcons(%q) should be false", in)
		}
	}
}

func TestDecodeEntities(t *testing.T) {
	if got := DecodeEntities("&lt;3 &amp;amp; &gt;"); got != "<3 &amp; >" {
		t.Errorf("got %q", got)
	}
}

func TestHasFormatting(t *testing.T) {
	if ToHTML("plain text", testOpts).HasFormatting {
		t.Error("plain text should not have formatting")
	}
	if !ToHTML("[b]x[/b]", testOpts).HasFormatting {
		t.Error("bold text should have formatting")
	}
}
