// Package bbcode converts F-Chat BBCode to Matrix HTML and plain text.
//
// Supported tags are the ones the F-Chat clients render in chat:
// b, i, u, s, sup, sub, color, url, user, icon, eicon, noparse, spoiler, session,
// plus a few profile tags (big, small, heading, quote, hr, center, left, right, justify, indent, collapse)
// that occasionally appear in channel descriptions.
package bbcode

import (
	"fmt"
	"html"
	"net/url"
	"strings"
)

// Options controls how links to F-List resources are generated.
type Options struct {
	// ProfileURL returns a link to a character profile.
	ProfileURL func(name string) string
	// EIconURL returns the image URL of an eicon.
	EIconURL func(name string) string
	// UserMention can optionally return a Matrix user ID to render [user] and
	// [icon] tags as Matrix pills. If it returns "", a profile link is used.
	UserMention func(name string) string
}

// Result is the output of a conversion.
type Result struct {
	HTML  string
	Plain string
	// HasFormatting is true if the HTML output differs meaningfully from the plain text.
	HasFormatting bool
}

// Colors supported by F-Chat's [color] tag, mapped to the hex values the official client uses.
var Colors = map[string]string{
	"red":    "#ff0000",
	"blue":   "#0000ff",
	"white":  "#ffffff",
	"yellow": "#ffff00",
	"pink":   "#ffc0cb",
	"gray":   "#808080",
	"green":  "#00ff00",
	"orange": "#ffa500",
	"purple": "#800080",
	"black":  "#000000",
	"brown":  "#a52a2a",
	"cyan":   "#00ffff",
}

// Tags whose content is taken literally instead of being parsed.
var rawContentTags = map[string]bool{
	"noparse": true,
	"user":    true,
	"icon":    true,
	"eicon":   true,
	"session": true,
}

var knownTags = map[string]bool{
	"b": true, "i": true, "u": true, "s": true, "sup": true, "sub": true,
	"color": true, "url": true, "spoiler": true,
	"big": true, "small": true, "heading": true, "quote": true,
	"center": true, "left": true, "right": true, "justify": true, "indent": true, "collapse": true,
	"noparse": true, "user": true, "icon": true, "eicon": true, "session": true,
	"hr": true,
}

type node struct {
	tag      string // "" for text nodes and the root
	arg      string
	text     string
	children []*node
}

type tagToken struct {
	name    string
	arg     string
	closing bool
	raw     string
}

// parseTag tries to parse a tag starting at s[0] == '['. It returns the token and its length.
func parseTag(s string) (tagToken, int, bool) {
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return tagToken{}, 0, false
	}
	inner := s[1:end]
	if strings.ContainsAny(inner, "[\n") {
		return tagToken{}, 0, false
	}
	tok := tagToken{raw: s[:end+1]}
	if strings.HasPrefix(inner, "/") {
		tok.closing = true
		inner = inner[1:]
	}
	name, arg, hasArg := strings.Cut(inner, "=")
	tok.name = strings.ToLower(strings.TrimSpace(name))
	if hasArg {
		if tok.closing {
			return tagToken{}, 0, false
		}
		tok.arg = strings.TrimSpace(arg)
	}
	if !knownTags[tok.name] {
		return tagToken{}, 0, false
	}
	return tok, end + 1, true
}

// findClosing finds the closing tag for a raw-content tag, case-insensitively.
func findClosing(s, name string) int {
	return strings.Index(strings.ToLower(s), "[/"+name+"]")
}

func parse(input string) *node {
	root := &node{}
	stack := []*node{root}
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			top := stack[len(stack)-1]
			top.children = append(top.children, &node{text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(input); {
		if input[i] != '[' {
			next := strings.IndexByte(input[i:], '[')
			if next < 0 {
				text.WriteString(input[i:])
				break
			}
			text.WriteString(input[i : i+next])
			i += next
			continue
		}
		tok, length, ok := parseTag(input[i:])
		if !ok {
			text.WriteByte('[')
			i++
			continue
		}
		if tok.closing {
			// Find the matching open tag in the stack.
			idx := -1
			for j := len(stack) - 1; j > 0; j-- {
				if stack[j].tag == tok.name {
					idx = j
					break
				}
			}
			if idx < 0 {
				text.WriteString(tok.raw)
				i += length
				continue
			}
			flush()
			stack = stack[:idx]
			i += length
			continue
		}
		if tok.name == "hr" {
			flush()
			top := stack[len(stack)-1]
			top.children = append(top.children, &node{tag: "hr"})
			i += length
			continue
		}
		if rawContentTags[tok.name] || (tok.name == "url" && tok.arg == "") {
			rest := input[i+length:]
			closeIdx := findClosing(rest, tok.name)
			if closeIdx < 0 {
				text.WriteString(tok.raw)
				i += length
				continue
			}
			flush()
			top := stack[len(stack)-1]
			top.children = append(top.children, &node{
				tag:      tok.name,
				arg:      tok.arg,
				children: []*node{{text: rest[:closeIdx]}},
			})
			i += length + closeIdx + len(tok.name) + 3
			continue
		}
		flush()
		n := &node{tag: tok.name, arg: tok.arg}
		top := stack[len(stack)-1]
		top.children = append(top.children, n)
		stack = append(stack, n)
		i += length
	}
	flush()
	return root
}

func isSafeURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	}
	return false
}

type renderer struct {
	opts          Options
	html          strings.Builder
	plain         strings.Builder
	hasFormatting bool
}

func escapeWithBreaks(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}

func nodeText(n *node) string {
	var b strings.Builder
	var walk func(*node)
	walk = func(n *node) {
		if n.tag == "" && n.children == nil {
			b.WriteString(n.text)
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func (r *renderer) text(s string) {
	r.html.WriteString(escapeWithBreaks(s))
	r.plain.WriteString(s)
}

func (r *renderer) wrap(open, closeTag string, n *node) {
	r.hasFormatting = true
	r.html.WriteString(open)
	r.children(n)
	r.html.WriteString(closeTag)
}

func (r *renderer) children(n *node) {
	for _, c := range n.children {
		r.render(c)
	}
}

func (r *renderer) userLink(name, prefix string) {
	name = strings.TrimSpace(name)
	r.hasFormatting = true
	if r.opts.UserMention != nil {
		if mxid := r.opts.UserMention(name); mxid != "" {
			fmt.Fprintf(&r.html, `<a href="https://matrix.to/#/%s">%s</a>`, html.EscapeString(mxid), html.EscapeString(name))
			r.plain.WriteString(prefix + name)
			return
		}
	}
	if r.opts.ProfileURL != nil {
		fmt.Fprintf(&r.html, `<a href="%s">%s</a>`, html.EscapeString(r.opts.ProfileURL(name)), html.EscapeString(prefix+name))
	} else {
		r.html.WriteString(html.EscapeString(prefix + name))
	}
	r.plain.WriteString(prefix + name)
}

func (r *renderer) render(n *node) {
	if n.tag == "" {
		if n.children == nil {
			r.text(n.text)
		} else {
			r.children(n)
		}
		return
	}
	switch n.tag {
	case "b":
		r.wrap("<strong>", "</strong>", n)
	case "i":
		r.wrap("<em>", "</em>", n)
	case "u":
		r.wrap("<u>", "</u>", n)
	case "s":
		r.wrap("<del>", "</del>", n)
	case "sup":
		r.wrap("<sup>", "</sup>", n)
	case "sub":
		r.wrap("<sub>", "</sub>", n)
	case "big", "heading":
		r.wrap("<strong>", "</strong>", n)
	case "small":
		r.wrap("<small>", "</small>", n)
	case "quote":
		r.hasFormatting = true
		r.html.WriteString("<blockquote>")
		r.plain.WriteString("> ")
		r.children(n)
		r.html.WriteString("</blockquote>")
		r.plain.WriteString("\n")
	case "spoiler":
		r.hasFormatting = true
		r.html.WriteString("<span data-mx-spoiler>")
		r.plain.WriteString("||")
		r.children(n)
		r.plain.WriteString("||")
		r.html.WriteString("</span>")
	case "color":
		hex, ok := Colors[strings.ToLower(n.arg)]
		if !ok {
			r.children(n)
			return
		}
		r.wrap(fmt.Sprintf(`<font color="%s" data-mx-color="%s">`, hex, hex), "</font>", n)
	case "hr":
		r.hasFormatting = true
		r.html.WriteString("<hr>")
		r.plain.WriteString("\n---\n")
	case "url":
		var target, label string
		if n.arg != "" {
			target = n.arg
		} else {
			target = strings.TrimSpace(nodeText(n))
		}
		if !isSafeURL(target) {
			if n.arg != "" {
				r.children(n)
			} else {
				r.text(nodeText(n))
			}
			return
		}
		r.hasFormatting = true
		fmt.Fprintf(&r.html, `<a href="%s">`, html.EscapeString(target))
		if n.arg != "" {
			// Render the label without touching the plain output, so we can add the URL after it.
			sub := &renderer{opts: r.opts}
			sub.children(n)
			r.html.WriteString(sub.html.String())
			label = sub.plain.String()
		} else {
			r.html.WriteString(html.EscapeString(target))
			label = target
		}
		r.html.WriteString("</a>")
		if label == "" || label == target {
			r.plain.WriteString(target)
		} else {
			r.plain.WriteString(label + " (" + target + ")")
		}
	case "user":
		r.userLink(nodeText(n), "")
	case "icon":
		r.userLink(nodeText(n), "")
	case "eicon":
		name := strings.TrimSpace(nodeText(n))
		r.hasFormatting = true
		if r.opts.EIconURL != nil {
			fmt.Fprintf(&r.html, `<a href="%s">:%s:</a>`, html.EscapeString(r.opts.EIconURL(name)), html.EscapeString(name))
		} else {
			r.html.WriteString(html.EscapeString(":" + name + ":"))
		}
		r.plain.WriteString(":" + name + ":")
	case "session":
		title := n.arg
		id := strings.TrimSpace(nodeText(n))
		if title == "" {
			title = id
		}
		r.hasFormatting = true
		fmt.Fprintf(&r.html, `<strong>#%s</strong> (<code>%s</code>)`, html.EscapeString(title), html.EscapeString(id))
		r.plain.WriteString("#" + title + " (" + id + ")")
	case "noparse":
		r.text(nodeText(n))
	case "center", "left", "right", "justify", "indent", "collapse":
		if n.tag == "collapse" && n.arg != "" {
			r.hasFormatting = true
			r.html.WriteString("<strong>" + escapeWithBreaks(n.arg) + "</strong><br>")
			r.plain.WriteString(n.arg + "\n")
		}
		r.children(n)
	default:
		r.children(n)
	}
}

// ToHTML converts BBCode to Matrix HTML and a plain text fallback.
func ToHTML(input string, opts Options) Result {
	r := &renderer{opts: opts}
	r.render(parse(input))
	return Result{
		HTML:          r.html.String(),
		Plain:         r.plain.String(),
		HasFormatting: r.hasFormatting,
	}
}

// ToPlain strips all BBCode tags.
func ToPlain(input string) string {
	return ToHTML(input, Options{}).Plain
}

var entityDecoder = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// DecodeEntities reverses the HTML escaping that the F-Chat server applies to
// message texts, channel titles and descriptions (only &lt; &gt; and &amp;).
func DecodeEntities(s string) string {
	return entityDecoder.Replace(s)
}
