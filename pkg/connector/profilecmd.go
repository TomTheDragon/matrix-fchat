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
	maxProfileFavorites      = 10
	maxProfileDescription    = 400
	profileCommandAvatarSize = 64
)

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
	// Memo is the user's private memo about the character.
	Memo string
	// Match compares the kinks with the user's character (nil for the own character).
	Match *kinkMatch
}

// kinkSummary counts the kinks per choice and returns the names of the favorites.
func (v *profileView) kinkSummary() (counts map[string]int, favorites []string) {
	counts = make(map[string]int)
	for id, choice := range v.Data.Kinks {
		counts[choice]++
		if choice == "fave" {
			if name := v.KinkNames[id]; name != "" {
				favorites = append(favorites, name)
			}
		}
	}
	for _, kink := range v.Data.CustomKinks {
		counts[kink.Choice]++
		if kink.Choice == "fave" {
			favorites = append(favorites, strings.TrimSpace(html.UnescapeString(kink.Name)))
		}
	}
	slices.SortFunc(favorites, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return counts, favorites
}

func (v *profileView) render() *event.MessageEventContent {
	var plain, out strings.Builder
	esc := html.EscapeString

	// Header: avatar, name (linked to the profile), chat link and custom title.
	out.WriteString("<p>")
	if v.AvatarMXC != "" {
		fmt.Fprintf(&out, `<img src="%s" alt="%s" title="%s" width="%d" height="%d"> `,
			esc(v.AvatarMXC), esc(v.Name), esc(v.Name), profileCommandAvatarSize, profileCommandAvatarSize)
	}
	out.WriteString(renderName(v.Name, v.Gender, v.Profile, v.Chat))
	plain.WriteString(v.Name)
	if title := strings.TrimSpace(html.UnescapeString(v.Data.CustomTitle)); title != "" {
		fmt.Fprintf(&out, " – <em>%s</em>", esc(title))
		plain.WriteString(" – " + title)
	}
	plain.WriteString("\n")

	// Online status.
	statusLine := "Offline"
	if v.Status != "" {
		statusLine = "Online: " + string(v.Status)
		if msg := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(v.StatusMsg))); msg != "" {
			statusLine += " – " + truncateRunes(msg, maxBioStatusLength)
		}
	}
	fmt.Fprintf(&out, "<br>%s</p>", esc(statusLine))
	plain.WriteString(statusLine + "\n")

	// Profile summary (gender, species, age, orientation, role, looking for).
	if summary := profileSummary(v.Data, v.ListItems); summary != "" {
		lines := strings.Split(summary, "\n")
		escaped := make([]string, len(lines))
		for i, line := range lines {
			escaped[i] = esc(line)
		}
		fmt.Fprintf(&out, "<p>%s</p>", strings.Join(escaped, "<br>"))
		plain.WriteString("\n" + summary + "\n")
	}

	// Kinks.
	counts, favorites := v.kinkSummary()
	if len(counts) > 0 {
		kinkLine := fmt.Sprintf("Kinks: %d favorite, %d yes, %d maybe, %d no",
			counts["fave"], counts["yes"], counts["maybe"], counts["no"])
		out.WriteString("<p>" + esc(kinkLine))
		plain.WriteString("\n" + kinkLine + "\n")
		if len(favorites) > 0 {
			shown := favorites
			more := ""
			if len(shown) > maxProfileFavorites {
				shown = shown[:maxProfileFavorites]
				more = fmt.Sprintf(" and %d more", len(favorites)-maxProfileFavorites)
			}
			favLine := "Favorites: " + strings.Join(shown, ", ") + more
			out.WriteString("<br>" + esc(favLine))
			plain.WriteString(favLine + "\n")
		}
		out.WriteString("</p>")
	}

	// Kink match with the user's character.
	if v.Match != nil && (v.Match.Shared > 0 || v.Match.Conflicts > 0) {
		matchLine := v.Match.summary()
		out.WriteString("<p>" + esc(matchLine))
		plain.WriteString(matchLine + "\n")
		if len(v.Match.BothFavorites) > 0 {
			shown := v.Match.BothFavorites
			more := ""
			if len(shown) > maxProfileFavorites {
				shown = shown[:maxProfileFavorites]
				more = fmt.Sprintf(" and %d more", len(v.Match.BothFavorites)-maxProfileFavorites)
			}
			bothLine := "Favorites of both: " + strings.Join(shown, ", ") + more
			out.WriteString("<br>" + esc(bothLine))
			plain.WriteString(bothLine + "\n")
		}
		out.WriteString("</p>")
	}

	// The user's memo.
	if v.Memo != "" {
		fmt.Fprintf(&out, "<p><strong>Your memo:</strong> %s</p>", strings.ReplaceAll(esc(v.Memo), "\n", "<br>"))
		plain.WriteString("\nYour memo: " + v.Memo + "\n")
	}

	// Start of the description.
	desc := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(v.Data.Description)))
	desc = strings.Join(strings.Fields(desc), " ")
	if desc != "" {
		desc = truncateRunes(desc, maxProfileDescription)
		fmt.Fprintf(&out, "<blockquote>%s</blockquote>", esc(desc))
		plain.WriteString("\n> " + desc + "\n")
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
	out.WriteString("<p>")
	if len(facts) > 0 {
		factLine := "Profile " + strings.Join(facts, ", ")
		out.WriteString(esc(factLine) + "<br>")
		plain.WriteString("\n" + factLine + "\n")
	}
	fmt.Fprintf(&out, `<code><a href="%s">Full profile on F-List</a></code></p>`, esc(v.Profile))
	plain.WriteString(v.Profile)

	content := &event.MessageEventContent{
		MsgType:       event.MsgNotice,
		Body:          plain.String(),
		Format:        event.FormatHTML,
		FormattedBody: out.String(),
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
	percent := 0
	if total := m.Shared + m.Conflicts; total > 0 {
		percent = (m.Shared*100 + total/2) / total
	}
	return fmt.Sprintf("Kink match with %s: %d%% (%d liked by both, %d conflicts)", m.Own, percent, m.Shared, m.Conflicts)
}
