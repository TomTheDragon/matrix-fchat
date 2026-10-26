package connector

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

var HelpSectionFChat = commands.HelpSection{Name: "F-Chat", Order: 25}

func (fc *FChatConnector) commandHandlers() []commands.CommandHandler {
	return []commands.CommandHandler{
		&commands.FullHandler{
			Func: fc.cmdJoin,
			Name: "join",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Join an F-Chat channel. Public channels are joined by name, private rooms by their ID (ADH-…).",
				Args:        "<_channel name or ID_>",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func: fc.cmdLeave,
			Name: "leave",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Leave an F-Chat channel. Without arguments, leaves the channel of the current room.",
				Args:        "[_channel name or ID_]",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func:    fc.cmdChannels,
			Name:    "channels",
			Aliases: []string{"list-channels"},
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "List official public channels.",
				Args:        "[_filter_]",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func:    fc.cmdRooms,
			Name:    "rooms",
			Aliases: []string{"list-rooms"},
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "List open private rooms created by users.",
				Args:        "[_filter_]",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func:    fc.cmdBookmarks,
			Name:    "bookmarks",
			Aliases: []string{"list-bookmarks"},
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "List your F-List bookmarks with their avatars and online status.",
				Args:        "[_filter_]",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func:    fc.cmdFriends,
			Name:    "friends",
			Aliases: []string{"list-friends"},
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "List the friends of your character with their avatars and online status.",
				Args:        "[_filter_]",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func: fc.cmdCreateRoom,
			Name: "create-room",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Create a new private, invite-only F-Chat room.",
				Args:        "<_title_>",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func: fc.cmdStatus,
			Name: "status",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Set your F-Chat status (online, looking, busy, dnd, idle, away) and an optional status message.",
				Args:        "<_status_> [_message_]",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func:    fc.cmdRoll,
			Name:    "roll",
			Aliases: []string{"dice"},
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Roll dice in the current channel, e.g. `1d20` or `2d6+1d10+5`.",
				Args:        "<_dice_>",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func: fc.cmdBottle,
			Name: "bottle",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Spin the bottle in the current channel.",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func: fc.cmdAd,
			Name: "ad",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Post a roleplay ad in the current channel (at most one every 10 minutes).",
				Args:        "<_text_>",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func: fc.cmdInvite,
			Name: "invite",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Invite a character to the current private room.",
				Args:        "<_character_>",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func: fc.cmdIgnore,
			Name: "ignore",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Add a character to your F-Chat ignore list, or remove them with `ignore remove <character>`.",
				Args:        "[remove] <_character_>",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func: fc.cmdReconnect,
			Name: "reconnect",
			Help: commands.HelpMeta{
				Section:     HelpSectionFChat,
				Description: "Reconnect to F-Chat, e.g. after being disconnected by the server.",
			},
			RequiresLogin: true,
		},
	}
}

// clientFor returns the F-Chat client to use for a command: the portal's login
// when used in a portal room, otherwise the user's default login.
func (fc *FChatConnector) clientFor(ce *commands.Event) *FChatClient {
	if ce.Portal != nil && ce.Portal.Receiver != "" {
		login := fc.Bridge.GetCachedUserLoginByID(ce.Portal.Receiver)
		if login != nil && login.UserMXID == ce.User.MXID {
			if client, ok := login.Client.(*FChatClient); ok {
				return client
			}
		}
	}
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You're not logged in")
		return nil
	}
	client, ok := login.Client.(*FChatClient)
	if !ok {
		ce.Reply("Invalid login")
		return nil
	}
	return client
}

// connectedClientFor is like clientFor, but also checks the connection.
func (fc *FChatConnector) connectedClientFor(ce *commands.Event) *FChatClient {
	client := fc.clientFor(ce)
	if client == nil {
		return nil
	}
	if !client.Conn.IsConnected() {
		ce.Reply("%s is not connected to F-Chat right now", client.meta().Character)
		return nil
	}
	return client
}

// portalChannel returns the channel of the portal the command was sent in.
func portalChannel(ce *commands.Event) string {
	if ce.Portal == nil {
		return ""
	}
	channel, _, _ := parsePortalID(ce.Portal.ID)
	if channel == "" {
		ce.Reply("This command can only be used in channel rooms")
	}
	return channel
}

func (fc *FChatConnector) cmdJoin(ce *commands.Event) {
	channel := strings.TrimSpace(ce.RawArgs)
	if channel == "" {
		ce.Reply("**Usage:** `$cmdprefix join <channel name or ID>`")
		return
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if err := client.Conn.Send(ce.Ctx, fchat.CmdJoinChannel, &fchat.ChannelRequest{Channel: channel}); err != nil {
		ce.Reply("Failed to join channel: %v", err)
		return
	}
	ce.Reply("Joining `%s` as %s… The room will be created once the server confirms the join.", channel, client.meta().Character)
}

func (fc *FChatConnector) cmdLeave(ce *commands.Event) {
	channel := strings.TrimSpace(ce.RawArgs)
	if channel == "" {
		if ce.Portal == nil {
			ce.Reply("**Usage:** `$cmdprefix leave <channel name or ID>`, or use `$cmdprefix leave` in a channel room")
			return
		}
		if channel = portalChannel(ce); channel == "" {
			return
		}
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if err := client.Conn.Send(ce.Ctx, fchat.CmdLeaveChannel, &fchat.ChannelRequest{Channel: channel}); err != nil {
		ce.Reply("Failed to leave channel: %v", err)
		return
	}
	ce.Reply("Leaving `%s`…", channel)
}

func matchesFilter(filter string, values ...string) bool {
	if filter == "" {
		return true
	}
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), filter) {
			return true
		}
	}
	return false
}

const maxListedChannels = 50

func (fc *FChatConnector) cmdChannels(ce *commands.Event) {
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	var resp fchat.PublicChannelList
	if err := client.Conn.Request(ce.Ctx, fchat.CmdPublicChannels, nil, fchat.CmdPublicChannels, &resp); err != nil {
		ce.Reply("Failed to get channel list: %v", err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(ce.RawArgs))
	channels := slices.DeleteFunc(resp.Channels, func(ch fchat.PublicChannel) bool {
		return !matchesFilter(filter, ch.Name)
	})
	slices.SortFunc(channels, func(a, b fchat.PublicChannel) int {
		return cmp.Compare(b.Characters, a.Characters)
	})
	if len(channels) == 0 {
		ce.Reply("No channels found")
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Found %d public channels", len(channels))
	if len(channels) > maxListedChannels {
		fmt.Fprintf(&out, " (showing the %d largest)", maxListedChannels)
		channels = channels[:maxListedChannels]
	}
	out.WriteString(":\n\n")
	for _, ch := range channels {
		fmt.Fprintf(&out, "* **%s** (%d characters, mode: %s)\n", ch.Name, ch.Characters, ch.Mode)
	}
	out.WriteString("\nJoin with `$cmdprefix join <name>`")
	ce.Reply(out.String())
}

func (fc *FChatConnector) cmdRooms(ce *commands.Event) {
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	var resp fchat.OpenRoomList
	if err := client.Conn.Request(ce.Ctx, fchat.CmdOpenRooms, nil, fchat.CmdOpenRooms, &resp); err != nil {
		ce.Reply("Failed to get room list: %v", err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(ce.RawArgs))
	rooms := slices.DeleteFunc(resp.Channels, func(room fchat.OpenRoom) bool {
		return !matchesFilter(filter, bbcode.DecodeEntities(room.Title), room.Name)
	})
	slices.SortFunc(rooms, func(a, b fchat.OpenRoom) int {
		return cmp.Compare(b.Characters, a.Characters)
	})
	if len(rooms) == 0 {
		ce.Reply("No rooms found")
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Found %d open private rooms", len(rooms))
	if len(rooms) > maxListedChannels {
		fmt.Fprintf(&out, " (showing the %d largest)", maxListedChannels)
		rooms = rooms[:maxListedChannels]
	}
	out.WriteString(":\n\n")
	for _, room := range rooms {
		fmt.Fprintf(&out, "* **%s** – `%s` (%d characters)\n", bbcode.DecodeEntities(room.Title), room.Name, room.Characters)
	}
	out.WriteString("\nJoin with `$cmdprefix join <ID>`")
	ce.Reply(out.String())
}

func (fc *FChatConnector) cmdCreateRoom(ce *commands.Event) {
	title := strings.TrimSpace(ce.RawArgs)
	if title == "" {
		ce.Reply("**Usage:** `$cmdprefix create-room <title>`")
		return
	} else if len(title) > 64 {
		ce.Reply("Room titles may not be longer than 64 characters")
		return
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if err := client.Conn.Send(ce.Ctx, fchat.CmdCreateChannel, &fchat.CreateChannelRequest{Channel: title}); err != nil {
		ce.Reply("Failed to create room: %v", err)
		return
	}
	ce.Reply("Creating room… It will be bridged automatically once it's created.")
}

func (fc *FChatConnector) cmdStatus(ce *commands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `$cmdprefix status <online|looking|busy|dnd|idle|away> [message]`")
		return
	}
	newStatus := fchat.Status(strings.ToLower(ce.Args[0]))
	if !slices.Contains(fchat.SettableStatuses, newStatus) {
		ce.Reply("Invalid status. Valid statuses are: online, looking, busy, dnd, idle, away")
		return
	}
	message := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ce.RawArgs), ce.Args[0]))
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	err := client.Conn.Send(ce.Ctx, fchat.CmdStatus, &fchat.StatusRequest{Status: newStatus, StatusMsg: message})
	if err != nil {
		ce.Reply("Failed to set status: %v", err)
		return
	}
	ce.React("✅")
}

func (fc *FChatConnector) sendRoll(ce *commands.Event, dice string) {
	channel := portalChannel(ce)
	if channel == "" {
		return
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	// The server echoes the result to everyone in the channel, including us.
	err := client.Conn.SendMessage(ce.Ctx, fchat.CmdRoll, &fchat.RollRequest{Channel: channel, Dice: dice})
	if err != nil {
		ce.Reply("Failed to roll: %v", err)
	}
}

func (fc *FChatConnector) cmdRoll(ce *commands.Event) {
	dice := strings.ReplaceAll(strings.TrimSpace(ce.RawArgs), " ", "")
	if dice == "" {
		ce.Reply("**Usage:** `$cmdprefix roll <dice>`, e.g. `1d20` or `2d6+5`")
		return
	}
	fc.sendRoll(ce, dice)
}

func (fc *FChatConnector) cmdBottle(ce *commands.Event) {
	fc.sendRoll(ce, "bottle")
}

func (fc *FChatConnector) cmdAd(ce *commands.Event) {
	text := strings.TrimSpace(ce.RawArgs)
	if text == "" {
		ce.Reply("**Usage:** `$cmdprefix ad <text>`")
		return
	}
	channel := portalChannel(ce)
	if channel == "" {
		return
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if len(text) > client.Conn.AdMax() {
		ce.Reply("The ad is too long")
		return
	}
	err := client.Conn.SendMessage(ce.Ctx, fchat.CmdAd, &fchat.ChannelMessage{Channel: channel, Message: text})
	if err != nil {
		ce.Reply("Failed to post ad: %v", err)
		return
	}
	// The server doesn't echo ads back to the sender, so bridge it ourselves.
	if fc.Config.BridgeAds {
		content := client.convertText(text, event.MsgText, "[Ad] ")
		client.queueMessage(ce.Portal.PortalKey, client.meta().Character, content, false)
	} else {
		ce.React("✅")
	}
}

func (fc *FChatConnector) cmdInvite(ce *commands.Event) {
	name := strings.TrimSpace(ce.RawArgs)
	if name == "" {
		ce.Reply("**Usage:** `$cmdprefix invite <character>`")
		return
	}
	channel := portalChannel(ce)
	if channel == "" {
		return
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	err := client.Conn.Send(ce.Ctx, fchat.CmdChannelInvite, &fchat.ChannelCharacterRequest{Channel: channel, Character: name})
	if err != nil {
		ce.Reply("Failed to send invite: %v", err)
	}
}

func (fc *FChatConnector) cmdIgnore(ce *commands.Event) {
	action := "add"
	name := strings.TrimSpace(ce.RawArgs)
	if len(ce.Args) > 1 && strings.EqualFold(ce.Args[0], "remove") {
		action = "delete"
		name = strings.TrimSpace(strings.TrimPrefix(name, ce.Args[0]))
	}
	if name == "" {
		ce.Reply("**Usage:** `$cmdprefix ignore [remove] <character>`")
		return
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if err := client.Conn.Send(ce.Ctx, fchat.CmdIgnore, &fchat.IgnoreRequest{Action: action, Character: name}); err != nil {
		ce.Reply("Failed to update ignore list: %v", err)
		return
	}
	ce.React("✅")
}

func (fc *FChatConnector) cmdReconnect(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	client.Disconnect()
	client.Connect(ce.Ctx)
	ce.Reply("Reconnecting %s…", client.meta().Character)
}
