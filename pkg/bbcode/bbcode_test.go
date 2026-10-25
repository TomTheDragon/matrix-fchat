package bbcode

import (
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
		{"user", "[user]Some Name[/user]", `<a href="https://www.f-list.net/c/Some Name">Some Name</a>`, "Some Name"},
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
	res := ToHTML("hi [user]bob[/user]", opts)
	want := `hi <a href="https://matrix.to/#/@fchat_bob:example.com">bob</a>`
	if res.HTML != want {
		t.Errorf("got %q, want %q", res.HTML, want)
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
