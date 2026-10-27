package connector

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"

	"matrix-fchat/pkg/fchat"
)

// maxTimeoutMinutes is the longest channel timeout the F-Chat client allows.
const maxTimeoutMinutes = 90

// channelCommand returns the client and channel for a command that only works in channel rooms.
func (fc *FChatConnector) channelCommand(ce *commands.Event) (*FChatClient, string) {
	channel := portalChannel(ce)
	if channel == "" {
		return nil, ""
	}
	client := fc.connectedClientFor(ce)
	if client == nil {
		return nil, ""
	}
	return client, channel
}

// sendChannelCommand sends a command to F-Chat and reacts to the command message. The server
// answers with the change (or an error, which is shown as a notice).
func sendChannelCommand(ce *commands.Event, client *FChatClient, code string, payload any) {
	if err := client.Conn.Send(ce.Ctx, code, payload); err != nil {
		ce.Reply("Failed to send the command: %v", err)
		return
	}
	ce.React("✅")
}

// characterModCommand returns a handler for a command that takes a character in a channel room.
func (fc *FChatConnector) characterModCommand(code, usage string) func(*commands.Event) {
	return func(ce *commands.Event) {
		name := strings.TrimSpace(ce.RawArgs)
		if name == "" {
			ce.Reply("**Usage:** `$cmdprefix %s`", usage)
			return
		}
		client, channel := fc.channelCommand(ce)
		if client == nil {
			return
		}
		sendChannelCommand(ce, client, code, &fchat.ChannelCharacterRequest{Channel: channel, Character: name})
	}
}

func (fc *FChatConnector) cmdTimeout(ce *commands.Event) {
	const usage = "**Usage:** `$cmdprefix timeout <minutes> <character>` (1 to 90 minutes)"
	if len(ce.Args) < 2 {
		ce.Reply(usage)
		return
	}
	minutes, err := strconv.Atoi(ce.Args[0])
	if err != nil || minutes < 1 || minutes > maxTimeoutMinutes {
		ce.Reply(usage)
		return
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ce.RawArgs), ce.Args[0]))
	client, channel := fc.channelCommand(ce)
	if client == nil {
		return
	}
	sendChannelCommand(ce, client, fchat.CmdChannelTimeout, &fchat.ChannelTimeoutRequest{Channel: channel, Character: name, Length: minutes})
}

func (fc *FChatConnector) cmdBanList(ce *commands.Event) {
	client, channel := fc.channelCommand(ce)
	if client == nil {
		return
	}
	// The server answers with a SYS message, which is posted into the room.
	if err := client.Conn.Send(ce.Ctx, fchat.CmdChannelBanList, &fchat.ChannelBanListRequest{Channel: channel}); err != nil {
		ce.Reply("Failed to request the ban list: %v", err)
	}
}

func (fc *FChatConnector) cmdChannelMode(ce *commands.Event) {
	mode := fchat.ChannelMode(strings.ToLower(strings.TrimSpace(ce.RawArgs)))
	if mode != fchat.ModeChat && mode != fchat.ModeAds && mode != fchat.ModeBoth {
		ce.Reply("**Usage:** `$cmdprefix channel-mode <chat|ads|both>` – which messages the channel accepts")
		return
	}
	client, channel := fc.channelCommand(ce)
	if client == nil {
		return
	}
	sendChannelCommand(ce, client, fchat.CmdRoomMode, &fchat.RoomModeChange{Channel: channel, Mode: mode})
}

func (fc *FChatConnector) cmdRoomVisibility(ce *commands.Event) {
	status := strings.ToLower(strings.TrimSpace(ce.RawArgs))
	switch status {
	case "open":
		status = "public"
	case "closed", "invite":
		status = "private"
	}
	if status != "public" && status != "private" {
		ce.Reply("**Usage:** `$cmdprefix room-visibility <public|private>` – whether everyone can find and join " +
			"your private room, or only invited characters")
		return
	}
	client, channel := fc.channelCommand(ce)
	if client == nil {
		return
	}
	if !fchat.IsPrivateChannelID(channel) {
		ce.Reply("Only private rooms (ADH-…) can be opened or closed")
		return
	}
	sendChannelCommand(ce, client, fchat.CmdRoomStatus, &fchat.RoomStatusRequest{Channel: channel, Status: status})
}

func (fc *FChatConnector) cmdServerInfo(ce *commands.Event) {
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	var upt fchat.Uptime
	if err := client.Conn.Request(ce.Ctx, fchat.CmdUptime, nil, fchat.CmdUptime, &upt); err != nil {
		ce.Reply("Failed to get the server info: %v", err)
		return
	}
	ce.Reply(formatUptime(&upt, time.Now()))
}

func formatUptime(upt *fchat.Uptime, now time.Time) string {
	started := time.Unix(upt.StartTime, 0).UTC()
	uptime := now.Sub(started).Truncate(time.Minute)
	days := int(uptime.Hours()) / 24
	hours := int(uptime.Hours()) % 24
	return fmt.Sprintf("**F-Chat server**\n\n"+
		"* Characters online: %d (record: %d)\n"+
		"* Channels: %d\n"+
		"* Running since %s (%d days, %d hours)\n"+
		"* Connections accepted since then: %d",
		upt.Users, upt.MaxUsers, upt.Channels, started.Format("2006-01-02 15:04 UTC"), days, hours, upt.Accepted)
}
