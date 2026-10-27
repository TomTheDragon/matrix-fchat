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
	if name == "" && ce.Portal != nil {
		if _, dmUser, ok := parsePortalID(ce.Portal.ID); ok && dmUser != "" {
			name = client.characterName(dmUser)
			if name == "" {
				if ghost, _ := fc.Bridge.GetExistingGhostByID(ce.Ctx, dmUser); ghost != nil && ghost.Name != "" {
					name = ghost.Name
				} else {
					name = string(dmUser)
				}
			}
		}
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
	view := &profileView{
		Name:      name,
		Profile:   fchat.ProfileURL(c.Main.Config.SiteURL, name),
		Chat:      c.dmLink(name),
		AvatarMXC: string(c.avatarMXC(ctx, name)),
		Data:      data,
		ListItems: opts.ListItems,
		KinkNames: opts.KinkNames,
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
