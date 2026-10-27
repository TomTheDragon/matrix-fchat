package connector

import (
	"context"
	"fmt"
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

// onlineAlertsEnabled returns the character's online alert setting, falling back to the config.
func (c *FChatClient) onlineAlertsEnabled() bool {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	if setting := c.meta().OnlineAlerts; setting != nil {
		return *setting
	}
	return c.Main.Config.OnlineAlerts
}

// sendOnlineAlert tells the user that a friend or bookmark came online.
func (c *FChatClient) sendOnlineAlert(userID networkid.UserID, name string, status fchat.Status) {
	if !c.onlineAlertsEnabled() {
		return
	}
	c.stateLock.Lock()
	_, isFriend := c.friends[userID]
	_, isBookmark := c.bookmarks[userID]
	last := c.lastOnlineAlert[userID]
	if (!isFriend && !isBookmark) || time.Since(last) < onlineAlertCooldown {
		c.stateLock.Unlock()
		return
	}
	c.lastOnlineAlert[userID] = time.Now()
	c.stateLock.Unlock()
	kind := "Your friend"
	if !isFriend {
		kind = "Your bookmark"
	}
	text := fmt.Sprintf("🟢 %s %s is now online", kind, name)
	if status != "" && status != fchat.StatusOnline {
		text += fmt.Sprintf(" (%s)", status)
	}
	c.sendDMNotice(c.ctx(), userID, text, "")
}

const onlineAlertsUsage = "**Usage:** `$cmdprefix online-alerts [on|off|default]` – get a notice when a friend or bookmark comes online"

func (fc *FChatConnector) cmdOnlineAlerts(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	character := client.meta().Character
	if len(ce.Args) == 0 {
		msg := fmt.Sprintf("Online alerts for **%s**: %s", character, onOff(client.onlineAlertsEnabled()))
		if client.meta().OnlineAlerts == nil {
			msg += " (bridge default)"
		}
		ce.Reply(msg + "\n\n" + onlineAlertsUsage)
		return
	}
	setting, ok := parseAdSetting(ce.Args[0])
	if !ok || len(ce.Args) > 1 {
		ce.Reply(onlineAlertsUsage)
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
	ce.Reply("Online alerts for **%s** are now %s", character, onOff(client.onlineAlertsEnabled()))
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
