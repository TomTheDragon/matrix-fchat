package connector

import (
	"context"
	"errors"
	"fmt"
	"html"
	"slices"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

const (
	maxProfileFavorites      = 30
	maxProfileYes            = 20
	maxProfileDescription    = 800
	maxProfileFieldLength    = 300
	profileCommandAvatarSize = 64
)

// profileGroupOrder is the order of the infotag groups on F-List profiles: general details,
// RP preferences, sexual details and contact details/sites.
var profileGroupOrder = []string{"3", "5", "2", "1"}

// profileInfotag is a profile field from the F-List mapping list.
type profileInfotag struct {
	ID, Name string
	IsList   bool
	Group    string
}

// cmdProfile shows a short overview of a character's F-List profile. The reply is sent by
// the bridge bot into the room the command was used in; portal rooms only have the user
// and ghosts as members, so only the user sees it.
func (fc *FChatConnector) cmdProfile(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	name := strings.TrimSpace(ce.RawArgs)
	if name == "" {
		name = fc.dmPartnerName(ce, client)
	}
	if name == "" {
		ce.Reply("Usage: `$cmdprefix profile <character>` (in a private chat, the character is optional)")
		return
	}
	content, err := client.profileOverview(ce.Ctx, name)
	if err != nil {
		var apiErr *fchat.APIError
		if errors.As(err, &apiErr) {
			ce.Reply("F-List didn't return the profile of %s: %s", name, apiErr.Message)
		} else {
			ce.Reply("Failed to load the profile of %s: %v", name, err)
		}
		return
	}
	_, err = ce.Bot.SendMessage(ce.Ctx, ce.OrigRoomID, event.EventMessage, &event.Content{Parsed: content}, nil)
	if err != nil {
		ce.Log.Err(err).Msg("Failed to send profile overview")
	}
}

// profileOverview loads a character's profile and renders the overview.
func (c *FChatClient) profileOverview(ctx context.Context, name string) (*event.MessageEventContent, error) {
	opts, err := c.Main.getSearchOptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load the F-List mapping list: %w", err)
	}
	var data *fchat.CharacterData
	err = c.withTicket(ctx, func(account, ticket string) error {
		var reqErr error
		data, reqErr = c.Main.API.GetCharacterData(ctx, account, ticket, name)
		return reqErr
	})
	if err != nil {
		return nil, err
	}
	if data.Name != "" {
		name = data.Name
	}
	userID := makeUserID(name)
	var memo string
	err = c.withTicket(ctx, func(account, ticket string) error {
		var reqErr error
		memo, reqErr = c.Main.API.GetMemo(ctx, account, ticket, name)
		return reqErr
	})
	if err != nil {
		c.log.Debug().Err(err).Str("character", name).Msg("Failed to load memo")
	}
	view := &profileView{
		Name:      name,
		Profile:   fchat.ProfileURL(c.Main.Config.SiteURL, name),
		Chat:      c.dmLink(name),
		AvatarMXC: string(c.avatarMXC(ctx, name)),
		Data:      data,
		ListItems: opts.ListItems,
		KinkNames: opts.KinkNames,
		Infotags:  opts.Infotags,
		Groups:    opts.InfotagGroups,
		Memo:      strings.TrimSpace(bbcode.DecodeEntities(memo)),
	}
	if userID != c.ownID {
		if own, err := c.ownCharacterData(ctx); err != nil {
			c.log.Debug().Err(err).Msg("Failed to load own profile for the kink match")
		} else {
			view.Match = compareKinks(own.Kinks, data.Kinks, opts.KinkNames)
			view.Match.Own = c.meta().Character
		}
	}
	c.stateLock.RLock()
	if info, ok := c.characters[userID]; ok && info.Status != "" && info.Status != fchat.StatusOffline {
		view.Status, view.StatusMsg = info.Status, info.StatusMsg
	}
	c.stateLock.RUnlock()
	view.Gender = c.characterGender(ctx, userID)
	return view.render(), nil
}

// profileView is the data of a profile overview.
type profileView struct {
	Name, Profile, Chat, AvatarMXC string
	Gender                         string
	Status                         fchat.Status
	StatusMsg                      string
	Data                           *fchat.CharacterData
	ListItems, KinkNames           map[string]string
	Infotags                       []profileInfotag
	// Groups maps infotag group IDs to their names.
	Groups map[string]string
	// Memo is the user's private memo about the character.
	Memo string
	// Match compares the kinks with the user's character (nil for the own character).
	Match *kinkMatch
}

// profileField is a filled-in profile field.
type profileField struct{ Name, Value string }

// fieldGroup is the filled-in fields of an infotag group.
type fieldGroup struct {
	Name   string
	Fields []profileField
}

// fieldGroups returns the filled-in profile fields, grouped like on the F-List profile page.
func (v *profileView) fieldGroups() []fieldGroup {
	byGroup := make(map[string][]profileField)
	for _, tag := range v.Infotags {
		raw := strings.TrimSpace(v.Data.Infotags[tag.ID])
		if tag.IsList {
			raw = v.ListItems[raw]
		}
		value := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(raw)))
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			continue
		}
		byGroup[tag.Group] = append(byGroup[tag.Group], profileField{Name: tag.Name, Value: truncateRunes(value, maxProfileFieldLength)})
	}
	var groups []fieldGroup
	addGroup := func(id string) {
		if fields := byGroup[id]; len(fields) > 0 {
			name := v.Groups[id]
			if name == "" {
				name = "Details"
			}
			groups = append(groups, fieldGroup{Name: name, Fields: fields})
			delete(byGroup, id)
		}
	}
	for _, id := range profileGroupOrder {
		addGroup(id)
	}
	rest := make([]string, 0, len(byGroup))
	for id := range byGroup {
		rest = append(rest, id)
	}
	slices.Sort(rest)
	for _, id := range rest {
		addGroup(id)
	}
	return groups
}

// kinkLists counts the kinks per choice and returns the names of the favorite and "yes" kinks.
func (v *profileView) kinkLists() (counts map[string]int, favorites, yes []string) {
	counts = make(map[string]int)
	add := func(choice, name string) {
		counts[choice]++
		if name == "" {
			return
		}
		switch choice {
		case "fave":
			favorites = append(favorites, name)
		case "yes":
			yes = append(yes, name)
		}
	}
	for id, choice := range v.Data.Kinks {
		add(choice, v.KinkNames[id])
	}
	for _, kink := range v.Data.CustomKinks {
		add(kink.Choice, strings.TrimSpace(html.UnescapeString(kink.Name)))
	}
	byName := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	slices.SortFunc(favorites, byName)
	slices.SortFunc(yes, byName)
	return counts, favorites, yes
}

// limitList joins up to max names, adding "and N more".
func limitList(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:limit], ", ") + fmt.Sprintf(" and %d more", len(names)-limit)
}

// profileWriter writes a profile overview as plain text and HTML.
type profileWriter struct {
	plain, html strings.Builder
}

// section writes a paragraph with an optional bold title and lines.
func (w *profileWriter) section(title string, lines ...string) {
	if len(lines) == 0 {
		return
	}
	w.html.WriteString("<p>")
	w.plain.WriteString("\n")
	if title != "" {
		fmt.Fprintf(&w.html, "<strong>%s</strong>", html.EscapeString(title))
		w.plain.WriteString(title + "\n")
	}
	for i, line := range lines {
		if i > 0 || title != "" {
			w.html.WriteString("<br>")
		}
		w.html.WriteString(html.EscapeString(line))
		w.plain.WriteString(line + "\n")
	}
	w.html.WriteString("</p>")
}

func (v *profileView) render() *event.MessageEventContent {
	var w profileWriter
	esc := html.EscapeString

	// Header: avatar, name (linked to the profile), chat link and custom title.
	w.html.WriteString("<p>")
	if v.AvatarMXC != "" {
		fmt.Fprintf(&w.html, `<img src="%s" alt="%s" title="%s" width="%d" height="%d"> `,
			esc(v.AvatarMXC), esc(v.Name), esc(v.Name), profileCommandAvatarSize, profileCommandAvatarSize)
	}
	w.html.WriteString(renderName(v.Name, v.Gender, v.Profile, v.Chat))
	w.plain.WriteString(v.Name)
	if title := strings.TrimSpace(html.UnescapeString(v.Data.CustomTitle)); title != "" {
		fmt.Fprintf(&w.html, " – <em>%s</em>", esc(title))
		w.plain.WriteString(" – " + title)
	}
	w.plain.WriteString(" (" + v.Profile + ")\n")

	// Online status.
	statusLine := "Offline"
	if v.Status != "" {
		statusLine = "Online: " + string(v.Status)
		if msg := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(v.StatusMsg))); msg != "" {
			statusLine += " – " + truncateRunes(msg, maxBioStatusLength)
		}
	}
	fmt.Fprintf(&w.html, "<br>%s</p>", esc(statusLine))
	w.plain.WriteString(statusLine + "\n")

	// Profile fields, grouped like on F-List.
	for _, group := range v.fieldGroups() {
		lines := make([]string, len(group.Fields))
		for i, field := range group.Fields {
			lines[i] = field.Name + ": " + field.Value
		}
		w.section(group.Name, lines...)
	}

	// Kinks.
	counts, favorites, yes := v.kinkLists()
	if len(counts) > 0 {
		lines := []string{fmt.Sprintf("%d favorite, %d yes, %d maybe, %d no",
			counts["fave"], counts["yes"], counts["maybe"], counts["no"])}
		if len(favorites) > 0 {
			lines = append(lines, "Favorites: "+limitList(favorites, maxProfileFavorites))
		}
		if len(yes) > 0 {
			lines = append(lines, "Yes: "+limitList(yes, maxProfileYes))
		}
		w.section("Kinks", lines...)
	}

	// Kink match with the user's character.
	if v.Match != nil && (v.Match.Shared > 0 || v.Match.Conflicts > 0) {
		lines := []string{v.Match.summary()}
		if len(v.Match.BothFavorites) > 0 {
			lines = append(lines, "Favorites of both: "+limitList(v.Match.BothFavorites, maxProfileFavorites))
		}
		w.section("", lines...)
	}

	// The user's memo.
	if v.Memo != "" {
		fmt.Fprintf(&w.html, "<p><strong>Your memo:</strong> %s</p>", strings.ReplaceAll(esc(v.Memo), "\n", "<br>"))
		w.plain.WriteString("\nYour memo: " + v.Memo + "\n")
	}

	// Start of the description.
	desc := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(v.Data.Description)))
	desc = strings.Join(strings.Fields(desc), " ")
	if desc != "" {
		desc = truncateRunes(desc, maxProfileDescription)
		fmt.Fprintf(&w.html, "<blockquote>%s</blockquote>", esc(desc))
		w.plain.WriteString("\n> " + desc + "\n")
	}

	// Profile facts.
	var facts []string
	if v.Data.CreatedAt > 0 {
		facts = append(facts, "created "+time.Unix(v.Data.CreatedAt, 0).UTC().Format("2006-01-02"))
	}
	if v.Data.UpdatedAt > 0 {
		facts = append(facts, "updated "+time.Unix(v.Data.UpdatedAt, 0).UTC().Format("2006-01-02"))
	}
	if v.Data.Views > 0 {
		facts = append(facts, fmt.Sprintf("%d views", v.Data.Views))
	}
	if v.Data.Images > 0 {
		facts = append(facts, fmt.Sprintf("%d images", v.Data.Images))
	}
	if len(facts) > 0 {
		w.section("", "Profile "+strings.Join(facts, ", "))
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgNotice,
		Body:          strings.TrimSpace(w.plain.String()),
		Format:        event.FormatHTML,
		FormattedBody: w.html.String(),
		Mentions:      &event.Mentions{},
	}
	disableLinkPreviews(content)
	return content
}

// ownProfileMaxAge is how long the own character's profile is cached for kink matches.
const ownProfileMaxAge = time.Hour

// ownCharacterData returns the profile of the login's character, cached for an hour.
func (c *FChatClient) ownCharacterData(ctx context.Context) (*fchat.CharacterData, error) {
	c.ownProfileLock.Lock()
	defer c.ownProfileLock.Unlock()
	if c.ownProfile != nil && time.Since(c.ownProfileFetched) < ownProfileMaxAge {
		return c.ownProfile, nil
	}
	var data *fchat.CharacterData
	err := c.withTicket(ctx, func(account, ticket string) error {
		var reqErr error
		data, reqErr = c.Main.API.GetCharacterData(ctx, account, ticket, c.meta().Character)
		return reqErr
	})
	if err != nil {
		return nil, err
	}
	c.ownProfile, c.ownProfileFetched = data, time.Now()
	return data, nil
}

// kinkMatch compares the kinks of two characters.
type kinkMatch struct {
	Own string
	// Shared is the number of kinks both like (favorite or yes).
	Shared int
	// Conflicts is the number of kinks one likes and the other doesn't want.
	Conflicts int
	// BothFavorites are the names of the kinks that both have as favorites.
	BothFavorites []string
}

func likes(choice string) bool { return choice == "fave" || choice == "yes" }

// compareKinks compares the standard kinks (custom kinks can't be compared).
func compareKinks(own, theirs map[string]string, names map[string]string) *kinkMatch {
	match := &kinkMatch{}
	for id, mine := range own {
		other, ok := theirs[id]
		if !ok {
			continue
		}
		switch {
		case likes(mine) && likes(other):
			match.Shared++
			if mine == "fave" && other == "fave" && names[id] != "" {
				match.BothFavorites = append(match.BothFavorites, names[id])
			}
		case likes(mine) && other == "no", mine == "no" && likes(other):
			match.Conflicts++
		}
	}
	slices.SortFunc(match.BothFavorites, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return match
}

// summary is a line like "Match with Kira: 80% (40 kinks you both like, 10 conflicts)".
func (m *kinkMatch) summary() string {
	return fmt.Sprintf("Kink match with %s: %d%% (%d liked by both, %d conflicts)", m.Own, m.percent(), m.Shared, m.Conflicts)
}
