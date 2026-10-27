package connector

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"matrix-fchat/pkg/fchat"
)

// timerInterval is how often the bridge checks for idle logins and ads to post.
const timerInterval = 30 * time.Second

// onlineAlertCooldown keeps characters that reconnect often from causing lots of alerts.
const onlineAlertCooldown = 15 * time.Minute

var _ bridgev2.ReadReceiptHandlingNetworkAPI = (*FChatClient)(nil)

// startTimers runs the periodic checks of all logins.
func (fc *FChatConnector) startTimers() {
	go func() {
		ticker := time.NewTicker(timerInterval)
		defer ticker.Stop()
		for range ticker.C {
			for _, login := range fc.Bridge.GetAllCachedUserLogins() {
				client, ok := login.Client.(*FChatClient)
				if !ok || !client.Conn.IsConnected() {
					continue
				}
				client.checkIdle()
				client.postAutoAds()
			}
		}
	}()
}

// idleState is the automatic idle status of a login.
type idleState struct {
	// lastActivity is the last activity of the user on Matrix.
	lastActivity time.Time
	// active is set while the bridge set the status to idle.
	active    bool
	status    fchat.Status
	statusMsg string
}

// markActive records activity of the user on Matrix and restores the status if the bridge
// set it to idle.
func (c *FChatClient) markActive(ctx context.Context) {
	c.idleLock.Lock()
	c.idle.lastActivity = time.Now()
	restore := c.idle.active
	status, statusMsg := c.idle.status, c.idle.statusMsg
	c.idle.active = false
	c.idleLock.Unlock()
	if !restore {
		return
	}
	// Only restore if nobody changed the status in the meantime.
	if own := c.ownStatus(); own.Status != fchat.StatusIdle {
		return
	}
	c.log.Debug().Str("status", string(status)).Msg("Restoring status after automatic idle")
	if err := c.Conn.Send(ctx, fchat.CmdStatus, &fchat.StatusRequest{Status: status, StatusMsg: statusMsg}); err != nil {
		c.log.Warn().Err(err).Msg("Failed to restore status after automatic idle")
		return
	}
	c.setOwnStatus(status, statusMsg)
}

// setOwnStatus records a status the bridge set, in case the server doesn't echo it.
func (c *FChatClient) setOwnStatus(status fchat.Status, statusMsg string) {
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	info, ok := c.characters[c.ownID]
	if !ok {
		info = &characterInfo{Name: c.meta().Character}
		c.characters[c.ownID] = info
	}
	info.Status, info.StatusMsg = status, statusMsg
}

// resetIdle is called after connecting (the server resets the status) and when the user sets
// the status with a command.
func (c *FChatClient) resetIdle() {
	c.idleLock.Lock()
	c.idle.lastActivity = time.Now()
	c.idle.active = false
	c.idleLock.Unlock()
}

// ownStatus returns the character's current status.
func (c *FChatClient) ownStatus() characterInfo {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	if info, ok := c.characters[c.ownID]; ok {
		return *info
	}
	return characterInfo{Status: fchat.StatusOnline}
}

// checkIdle sets the status to idle when the user wasn't active for auto_idle_minutes.
func (c *FChatClient) checkIdle() {
	minutes := c.Main.Config.AutoIdleMinutes
	if minutes <= 0 {
		return
	}
	c.idleLock.Lock()
	defer c.idleLock.Unlock()
	if c.idle.active || c.idle.lastActivity.IsZero() || time.Since(c.idle.lastActivity) < time.Duration(minutes)*time.Minute {
		return
	}
	own := c.ownStatus()
	if own.Status != fchat.StatusOnline && own.Status != fchat.StatusLooking {
		return
	}
	err := c.Conn.Send(c.ctx(), fchat.CmdStatus, &fchat.StatusRequest{Status: fchat.StatusIdle, StatusMsg: own.StatusMsg})
	if err != nil {
		c.log.Warn().Err(err).Msg("Failed to set automatic idle status")
		return
	}
	c.log.Debug().Msg("Set status to idle after inactivity")
	c.setOwnStatus(fchat.StatusIdle, own.StatusMsg)
	c.idle.active, c.idle.status, c.idle.statusMsg = true, own.Status, own.StatusMsg
}

func (c *FChatClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	c.markActive(ctx)
	return nil
}

// Online alerts are decided by, in order: the setting of the private chat with the character
// (which works for any character), the setting of the logged-in character and the
// online_alerts config option (both only for friends and bookmarks).

// onlineAlertsEnabled returns the character's online alert setting, falling back to the config.
func (c *FChatClient) onlineAlertsEnabled() bool {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	if setting := c.meta().OnlineAlerts; setting != nil {
		return *setting
	}
	return c.Main.Config.OnlineAlerts
}

// chatOnlineAlert returns the online alert setting of the private chat with a character.
func (c *FChatClient) chatOnlineAlert(userID networkid.UserID) (enabled, ok bool) {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	enabled, ok = c.meta().ChatOnlineAlerts[string(userID)]
	return
}

// setChatOnlineAlert changes the online alert setting of a private chat (nil = default).
func (c *FChatClient) setChatOnlineAlert(ctx context.Context, userID networkid.UserID, setting *bool) error {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	meta := c.meta()
	if setting == nil {
		delete(meta.ChatOnlineAlerts, string(userID))
	} else {
		if meta.ChatOnlineAlerts == nil {
			meta.ChatOnlineAlerts = make(map[string]bool)
		}
		meta.ChatOnlineAlerts[string(userID)] = *setting
	}
	return c.UserLogin.Save(ctx)
}

// wantsOnlineAlert returns whether the user wants to know when a character comes online,
// and how the character is related to the user (for the alert text).
func (c *FChatClient) wantsOnlineAlert(userID networkid.UserID) (bool, string) {
	c.stateLock.RLock()
	_, isFriend := c.friends[userID]
	_, isBookmark := c.bookmarks[userID]
	c.stateLock.RUnlock()
	kind := ""
	switch {
	case isFriend:
		kind = "Your friend "
	case isBookmark:
		kind = "Your bookmark "
	}
	if enabled, ok := c.chatOnlineAlert(userID); ok {
		return enabled, kind
	}
	return (isFriend || isBookmark) && c.onlineAlertsEnabled(), kind
}

// sendOnlineAlert tells the user that a character they want alerts for came online.
func (c *FChatClient) sendOnlineAlert(userID networkid.UserID, name string, status fchat.Status) {
	want, kind := c.wantsOnlineAlert(userID)
	if !want {
		return
	}
	c.stateLock.Lock()
	last := c.lastOnlineAlert[userID]
	if time.Since(last) < onlineAlertCooldown {
		c.stateLock.Unlock()
		return
	}
	c.lastOnlineAlert[userID] = time.Now()
	c.stateLock.Unlock()
	text := fmt.Sprintf("🟢 %s%s is now online", kind, name)
	if status != "" && status != fchat.StatusOnline {
		text += fmt.Sprintf(" (%s)", status)
	}
	c.sendDMNotice(c.ctx(), userID, text, "")
}

const onlineAlertsUsage = "**Usage:**\n\n" +
	"* `$cmdprefix online-alerts character [on|off|default]` – alerts when a friend or bookmark of the character comes online\n" +
	"* `$cmdprefix online-alerts chat [on|off|default]` – in a private chat: alerts when that character comes online " +
	"(works for everyone, not only friends and bookmarks; `off` turns them off for a friend or bookmark)\n" +
	"* `$cmdprefix online-alerts for <character> [on|off|default]` – the same for any character, from any room\n\n" +
	"Without `character`, `chat` or `for`, a private chat changes the chat's setting and other rooms the character's setting."

func (fc *FChatConnector) cmdOnlineAlerts(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	args := ce.Args
	dmName := fc.dmPartnerName(ce, client)
	scope, target := "character", ""
	if dmName != "" {
		scope, target = "chat", dmName
	}
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "character", "char", "global", "all":
			scope, target, args = "character", "", args[1:]
		case "chat", "here", "room", "dm":
			if dmName == "" {
				ce.Reply("Use `$cmdprefix online-alerts chat …` in a private chat, or `$cmdprefix online-alerts for <character> …`")
				return
			}
			scope, target, args = "chat", dmName, args[1:]
		case "for":
			// The character name can contain spaces, the setting is the last word.
			rest := args[1:]
			if len(rest) > 1 {
				if _, ok := parseAdSetting(rest[len(rest)-1]); ok {
					target, args = strings.Join(rest[:len(rest)-1], " "), rest[len(rest)-1:]
				}
			}
			if target == "" {
				target, args = strings.Join(rest, " "), nil
			}
			if target == "" {
				ce.Reply(onlineAlertsUsage)
				return
			}
			scope = "chat"
		}
	}
	character := client.meta().Character

	if len(args) == 0 {
		ce.Reply(client.describeOnlineAlerts(scope, target) + "\n\n" + onlineAlertsUsage)
		return
	}
	setting, ok := parseAdSetting(args[0])
	if !ok || len(args) > 1 {
		ce.Reply(onlineAlertsUsage)
		return
	}

	if scope == "chat" {
		userID := makeUserID(target)
		if userID == "" || userID == client.ownID {
			ce.Reply("Invalid character name")
			return
		}
		if err := client.setChatOnlineAlert(ce.Ctx, userID, setting); err != nil {
			ce.Reply("Failed to save the setting: %v", err)
			return
		}
		want, _ := client.wantsOnlineAlert(userID)
		msg := fmt.Sprintf("Online alerts for **%s** are now %s", target, onOff(want))
		if setting == nil {
			msg += " (following the setting of " + character + ")"
		}
		ce.Reply(msg)
		return
	}

	client.metaLock.Lock()
	client.meta().OnlineAlerts = setting
	err := client.UserLogin.Save(ce.Ctx)
	client.metaLock.Unlock()
	if err != nil {
		ce.Reply("Failed to save the setting: %v", err)
		return
	}
	ce.Reply("Online alerts for friends and bookmarks of **%s** are now %s. Private chats with their own setting aren't affected.",
		character, onOff(client.onlineAlertsEnabled()))
}

// describeOnlineAlerts shows the current online alert settings.
func (c *FChatClient) describeOnlineAlerts(scope, target string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Online alerts for friends and bookmarks of **%s**: %s", c.meta().Character, onOff(c.onlineAlertsEnabled()))
	if c.meta().OnlineAlerts == nil {
		out.WriteString(" (bridge default)")
	}
	if scope == "chat" {
		userID := makeUserID(target)
		want, _ := c.wantsOnlineAlert(userID)
		fmt.Fprintf(&out, "\n\nOnline alerts for **%s**: %s", target, onOff(want))
		if _, ok := c.chatOnlineAlert(userID); !ok {
			out.WriteString(" (character setting)")
		}
	}
	c.metaLock.Lock()
	settings := maps.Clone(c.meta().ChatOnlineAlerts)
	c.metaLock.Unlock()
	overrides := make([]string, 0, len(settings))
	for userID, enabled := range settings {
		name := c.characterName(networkid.UserID(userID))
		if name == "" {
			name = userID
		}
		overrides = append(overrides, fmt.Sprintf("%s: %s", name, onOff(enabled)))
	}
	if len(overrides) > 0 {
		slices.Sort(overrides)
		out.WriteString("\n\nCharacters with their own setting: " + strings.Join(overrides, " · "))
	}
	return out.String()
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
