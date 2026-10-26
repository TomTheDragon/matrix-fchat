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
	// UserMention can optionally return a Matrix user ID that is mentioned by a [user] or
	// [icon] tag with that name (used to mention the Matrix user). See Result.Mentions.
	UserMention func(name string) string
	// UserLink can optionally return the link that opens a chat with a character and the
	// color of its name. [user] and [icon] tags get this link plus a 🏠 link to the profile.
	UserLink func(name string) (href, color string)
	// NoPreview can return true for links that clients shouldn't show previews for.
	// Those links are wrapped in <code>, which Element doesn't generate previews for.
	NoPreview func(href string) bool

	// The image functions return the mxc:// URI of an image to show inline.
	// If they're nil or return "", a text fallback is used.
	AvatarImage   func(name string) string
	EIconImage    func(name string) string
	EmoticonImage func(name string) string
}

// Emoticons are the smileys that F-List replaces in texts, e.g. ":heart:".
// The images are at static.f-list.net/images/smileys/<name>.png.
var Emoticons = []string{
	"heart", "cake",
	"lif-angry", "lif-blush", "lif-cry", "lif-evil", "lif-gasp", "lif-happy", "lif-meh", "lif-neutral",
	"lif-ooh", "lif-purr", "lif-roll", "lif-sad", "lif-sick", "lif-smile", "lif-whee", "lif-wink",
	"lif-wtf", "lif-yawn",
	"hex-smile", "hex-confuse", "hex-eek", "hex-mad", "hex-roll", "hex-wink", "hex-twist", "hex-sad",
	"hex-grin", "hex-red", "hex-razz", "hex-yell",
}

var emoticonSet = func() map[string]bool {
	set := make(map[string]bool, len(Emoticons))
	for _, name := range Emoticons {
		set[name] = true
	}
	return set
}()

// Display sizes of inline images, matching the F-Chat client.
const (
	iconSize     = 50
	emoticonSize = 21
)

// Result is the output of a conversion.
type Result struct {
	HTML  string
	Plain string
	// PreviewLinks are the http(s) links that clients may show previews for.
	PreviewLinks []string
	// Mentions are the Matrix user IDs returned by Options.UserMention.
	Mentions []string
	// HiddenPreviews is the number of links that shouldn't get a preview (see Options.NoPreview).
	HiddenPreviews int
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
	previewLinks  []string
	mentions      []string
	hiddenPreview int
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

// richText renders a text node and replaces emoticons like ":heart:" with images.
func (r *renderer) richText(s string) {
	if r.opts.EmoticonImage == nil {
		r.text(s)
		return
	}
	for {
		start := strings.IndexByte(s, ':')
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start+1:], ':')
		if end < 0 {
			break
		}
		end += start + 1
		code := s[start : end+1]
		name := strings.ToLower(code[1 : len(code)-1])
		if !emoticonSet[name] {
			// The closing colon might start the next emoticon.
			r.text(s[:end])
			s = s[end:]
			continue
		}
		mxc := r.opts.EmoticonImage(name)
		if mxc == "" {
			r.text(s[:end+1])
			s = s[end+1:]
			continue
		}
		r.text(s[:start])
		r.hasFormatting = true
		fmt.Fprintf(&r.html, `<img data-mx-emoticon src="%s" alt="%s" title="%s" height="%d">`,
			html.EscapeString(mxc), html.EscapeString(code), html.EscapeString(code), emoticonSize)
		r.plain.WriteString(code)
		s = s[end+1:]
	}
	r.text(s)
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

// link writes an opening <a> tag (and <code> for links without previews).
// The returned function writes the closing tags.
func (r *renderer) link(href string) (closeLink func()) {
	if r.opts.NoPreview != nil && r.opts.NoPreview(href) {
		r.hiddenPreview++
		fmt.Fprintf(&r.html, `<code><a href="%s">`, html.EscapeString(href))
		return func() { r.html.WriteString("</a></code>") }
	}
	if isSafeURL(href) && !strings.HasPrefix(href, "https://matrix.to/") {
		r.previewLinks = append(r.previewLinks, href)
	}
	fmt.Fprintf(&r.html, `<a href="%s">`, html.EscapeString(href))
	return func() { r.html.WriteString("</a>") }
}

// coloredName returns the escaped name, in a <font> tag if it has a color.
func coloredName(name, color string) string {
	label := html.EscapeString(name)
	if color != "" {
		label = fmt.Sprintf(`<font color="%s" data-mx-color="%s">%s</font>`, html.EscapeString(color), html.EscapeString(color), label)
	}
	return label
}

// characterLinks renders [user] and [icon] tags: the label (name or avatar) linked to the
// character's profile, followed by a 💬 link that opens the chat with the character.
//
// Both links are wrapped in <code>, because Element doesn't generate link previews there.
// There's no line break before or after them, so several can be on the same line.
func (r *renderer) characterLinks(name, label string) {
	r.hasFormatting = true
	r.plain.WriteString(name)
	if r.opts.UserMention != nil {
		if mxid := r.opts.UserMention(name); mxid != "" {
			r.mentions = append(r.mentions, mxid)
		}
	}
	var chat, profile string
	if r.opts.UserLink != nil {
		chat, _ = r.opts.UserLink(name)
	}
	if r.opts.ProfileURL != nil {
		profile = r.opts.ProfileURL(name)
	}
	if chat == "" && profile == "" {
		r.html.WriteString(label)
		return
	}
	r.html.WriteString("<code>")
	if profile != "" {
		r.hiddenPreview++
		fmt.Fprintf(&r.html, `<a href="%s">%s</a>`, html.EscapeString(profile), label)
	} else {
		r.html.WriteString(label)
	}
	if chat != "" {
		fmt.Fprintf(&r.html, ` <a href="%s">💬</a>`, html.EscapeString(chat))
	}
	r.html.WriteString("</code>")
}

// userLink renders a [user] tag.
func (r *renderer) userLink(name string) {
	name = strings.TrimSpace(name)
	var color string
	if r.opts.UserLink != nil {
		_, color = r.opts.UserLink(name)
	}
	r.characterLinks(name, coloredName(name, color))
}

// icon renders an [icon] tag with the avatar image.
func (r *renderer) icon(name, mxc string) {
	r.characterLinks(name, fmt.Sprintf(`<img src="%s" alt="%s" title="%s" width="%d" height="%d">`,
		html.EscapeString(mxc), html.EscapeString(name), html.EscapeString(name), iconSize, iconSize))
}

// image writes an inline image.
func (r *renderer) image(mxc, alt string, size int) {
	r.hasFormatting = true
	fmt.Fprintf(&r.html, `<img src="%s" alt="%s" title="%s" width="%d" height="%d">`,
		html.EscapeString(mxc), html.EscapeString(alt), html.EscapeString(alt), size, size)
}

func (r *renderer) render(n *node) {
	if n.tag == "" {
		if n.children == nil {
			r.richText(n.text)
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
		closeLink := r.link(target)
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
		closeLink()
		if label == "" || label == target {
			r.plain.WriteString(target)
		} else {
			r.plain.WriteString(label + " (" + target + ")")
		}
	case "user":
		r.userLink(nodeText(n))
	case "icon":
		name := strings.TrimSpace(nodeText(n))
		if r.opts.AvatarImage != nil {
			if mxc := r.opts.AvatarImage(name); mxc != "" {
				r.icon(name, mxc)
				return
			}
		}
		r.userLink(name)
	case "eicon":
		name := strings.TrimSpace(nodeText(n))
		r.hasFormatting = true
		if r.opts.EIconImage != nil {
			if mxc := r.opts.EIconImage(name); mxc != "" {
				r.image(mxc, name, iconSize)
				r.plain.WriteString(":" + name + ":")
				return
			}
		}
		if r.opts.EIconURL != nil {
			closeLink := r.link(r.opts.EIconURL(name))
			r.html.WriteString(html.EscapeString(":" + name + ":"))
			closeLink()
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

		PreviewLinks:   r.previewLinks,
		Mentions:       r.mentions,
		HiddenPreviews: r.hiddenPreview,
	}
}

// OnlyEIcons returns the eicon names if the text consists of nothing but [eicon] tags
// (and whitespace between them).
func OnlyEIcons(input string) ([]string, bool) {
	var names []string
	for _, n := range parse(input).children {
		switch {
		case n.tag == "eicon":
			name := strings.TrimSpace(nodeText(n))
			if name == "" {
				return nil, false
			}
			names = append(names, name)
		case n.tag == "" && n.children == nil && strings.TrimSpace(n.text) == "":
		default:
			return nil, false
		}
	}
	return names, len(names) > 0
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
