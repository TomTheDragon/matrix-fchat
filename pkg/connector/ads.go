package connector

import (
	"context"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
)

// Whether roleplay ads are bridged is decided by, in order: the room's setting,
// the character's setting and the bridge_ads config option.

// characterShowsAds returns the ad setting of the character, falling back to the config.
func (c *FChatClient) characterShowsAds() bool {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	if setting := c.meta().ShowAds; setting != nil {
		return *setting
	}
	return c.Main.Config.BridgeAds
}

// showAds returns whether ads in a channel are bridged.
func (c *FChatClient) showAds(ctx context.Context, channel string) bool {
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, makeChannelPortalKey(channel, c.UserLogin.ID))
	if err != nil {
		c.log.Err(err).Str("channel", channel).Msg("Failed to get portal to check ad setting")
	} else if portal != nil {
		if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta.ShowAds != nil {
			return *meta.ShowAds
		}
	}
	return c.characterShowsAds()
}

func describeAds(show bool) string {
	if show {
		return "shown"
	}
	return "hidden"
}

// parseAdSetting parses on/off/default. ok is false for anything else.
func parseAdSetting(arg string) (setting *bool, ok bool) {
	switch strings.ToLower(arg) {
	case "on", "show", "yes", "true", "enable":
		return ptrTo(true), true
	case "off", "hide", "no", "false", "disable":
		return ptrTo(false), true
	case "default", "reset", "inherit":
		return nil, true
	}
	return nil, false
}

const adsUsage = "**Usage:** `$cmdprefix ads [room|character] [on|off|default]`\n\n" +
	"In a channel room, `ads on`/`ads off` changes that room, " +
	"`default` makes it follow the character's setting again. " +
	"Elsewhere (or with `character`), it changes the setting of the character."

func (fc *FChatConnector) cmdAds(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	args := ce.Args
	channel := ""
	if ce.Portal != nil {
		channel, _, _ = parsePortalID(ce.Portal.ID)
	}
	scope := "room"
	if channel == "" {
		scope = "character"
	}
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "room", "here", "channel":
			if channel == "" {
				ce.Reply("Use `$cmdprefix ads room …` in a channel room")
				return
			}
			scope, args = "room", args[1:]
		case "character", "char", "global", "all":
			scope, args = "character", args[1:]
		}
	}
	character := client.meta().Character

	if len(args) == 0 {
		var out strings.Builder
		out.WriteString("Ads for **" + character + "**: " + describeAds(client.characterShowsAds()))
		if client.meta().ShowAds == nil {
			out.WriteString(" (bridge default)")
		}
		if channel != "" {
			meta := ce.Portal.Metadata.(*PortalMetadata)
			out.WriteString("\n\nAds in this room: " + describeAds(client.showAds(ce.Ctx, channel)))
			if meta.ShowAds == nil {
				out.WriteString(" (character setting)")
			}
		}
		out.WriteString("\n\n" + adsUsage)
		ce.Reply(out.String())
		return
	}
	setting, ok := parseAdSetting(args[0])
	if !ok || len(args) > 1 {
		ce.Reply(adsUsage)
		return
	}

	if scope == "room" {
		meta := ce.Portal.Metadata.(*PortalMetadata)
		meta.ShowAds = setting
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			ce.Reply("Failed to save the room setting: %v", err)
			return
		}
		if setting == nil {
			ce.Reply("Ads in this room now follow the character's setting (%s)", describeAds(client.characterShowsAds()))
		} else {
			ce.Reply("Ads in this room are now %s", describeAds(*setting))
		}
		return
	}

	client.metaLock.Lock()
	client.meta().ShowAds = setting
	err := client.UserLogin.Save(ce.Ctx)
	client.metaLock.Unlock()
	if err != nil {
		ce.Reply("Failed to save the character setting: %v", err)
		return
	}
	msg := "Ads for **" + character + "** are now " + describeAds(client.characterShowsAds())
	if setting == nil {
		msg += " (bridge default)"
	}
	ce.Reply(msg + ". Rooms with their own setting aren't affected.")
}
