package connector

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/fchat"
)

// Characters that go offline stay in the channel rooms. Instead of a leave, their ghost gets
// the Matrix presence "offline" (bridge_presence) and optionally a suffix on its name
// (offline_name_suffix), for homeservers without presence. Ghosts that stay offline longer
// than ghost_cleanup_days are removed from the channel rooms by the ghost cleanup.
//
// The time a ghost went offline is kept in its metadata (OfflineSince), so it survives
// restarts. All updates go through one worker, which merges updates for the same character.

const (
	// rejoinGrace is how long a character that came online may take to rejoin a channel it
	// was in before going offline. After that, it leaves the channel room.
	rejoinGrace = 2 * time.Minute
	// presenceSyncDelay is how long after the last online list (LIS) chunk all ghosts are
	// compared with the online list. The list arrives in several chunks.
	presenceSyncDelay = 30 * time.Second
)

type presenceUpdate struct {
	status    fchat.Status
	statusMsg string
}

type presenceTracker struct {
	lock    sync.Mutex
	pending map[networkid.UserID]presenceUpdate
	wake    chan struct{}
	// offline is whether a ghost is marked as offline, loaded from the ghost metadata.
	offline map[networkid.UserID]bool
}

func isOfflineStatus(status fchat.Status) bool {
	return status == "" || status == fchat.StatusOffline
}

// startPresence loads the offline state of all ghosts and starts the presence worker.
func (fc *FChatConnector) startPresence(ctx context.Context) {
	p := &fc.presence
	p.lock.Lock()
	p.pending = make(map[networkid.UserID]presenceUpdate)
	p.offline = make(map[networkid.UserID]bool)
	p.wake = make(chan struct{}, 1)
	p.lock.Unlock()
	if err := fc.loadOfflineGhosts(ctx); err != nil {
		fc.Bridge.Log.Err(err).Msg("Failed to load offline state of ghosts")
	}
	go fc.presenceWorker()
	if fc.Config.BridgePresence && fc.Config.PresenceRefreshSeconds > 0 {
		go fc.presenceRefreshLoop()
	}
}

func (fc *FChatConnector) loadOfflineGhosts(ctx context.Context) error {
	rows, err := fc.Bridge.DB.Query(ctx, "SELECT id, metadata FROM ghost WHERE bridge_id=$1", fc.Bridge.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	p := &fc.presence
	p.lock.Lock()
	defer p.lock.Unlock()
	for rows.Next() {
		var userID networkid.UserID
		var raw []byte
		if err = rows.Scan(&userID, &raw); err != nil {
			return err
		}
		var meta GhostMetadata
		if len(raw) > 0 && json.Unmarshal(raw, &meta) == nil && meta.OfflineSince != 0 && !meta.Removed {
			p.offline[userID] = true
		}
	}
	return rows.Err()
}

// isMarkedOffline returns whether a ghost is marked as offline.
func (fc *FChatConnector) isMarkedOffline(userID networkid.UserID) bool {
	fc.presence.lock.Lock()
	defer fc.presence.lock.Unlock()
	return fc.presence.offline[userID]
}

// offlineSuffix returns the suffix the name of a ghost should have.
func (fc *FChatConnector) offlineSuffix(userID networkid.UserID) string {
	if fc.Config.OfflineNameSuffix == "" || !fc.isMarkedOffline(userID) {
		return ""
	}
	return fc.Config.OfflineNameSuffix
}

// queuePresence records the new status of a character. The ghost is updated by the worker.
func (fc *FChatConnector) queuePresence(userID networkid.UserID, status fchat.Status, statusMsg string) {
	p := &fc.presence
	p.lock.Lock()
	if p.pending == nil {
		p.lock.Unlock()
		return
	}
	p.pending[userID] = presenceUpdate{status: status, statusMsg: statusMsg}
	p.lock.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (fc *FChatConnector) presenceWorker() {
	p := &fc.presence
	ctx := fc.Bridge.Log.With().Str("component", "presence").Logger().WithContext(context.Background())
	for range p.wake {
		for {
			p.lock.Lock()
			if len(p.pending) == 0 {
				p.lock.Unlock()
				break
			}
			var userID networkid.UserID
			var update presenceUpdate
			for userID, update = range p.pending {
				break
			}
			delete(p.pending, userID)
			p.lock.Unlock()
			fc.applyPresence(ctx, userID, update)
		}
	}
}

// applyPresence updates the offline state, name and presence of an existing ghost.
func (fc *FChatConnector) applyPresence(ctx context.Context, userID networkid.UserID, update presenceUpdate) {
	ghost, err := fc.Bridge.GetExistingGhostByID(ctx, userID)
	if err != nil || ghost == nil {
		return
	}
	meta, ok := ghost.Metadata.(*GhostMetadata)
	if !ok || meta.Removed {
		return
	}
	offline := isOfflineStatus(update.status)
	changed := false
	if offline && meta.OfflineSince == 0 {
		meta.OfflineSince = time.Now().Unix()
		changed = true
	} else if !offline && (meta.OfflineSince != 0 || meta.LeftChannels) {
		meta.OfflineSince = 0
		meta.LeftChannels = false
		changed = true
	}
	fc.presence.lock.Lock()
	if offline {
		fc.presence.offline[userID] = true
	} else {
		delete(fc.presence.offline, userID)
	}
	fc.presence.lock.Unlock()

	if ghost.Name != "" {
		base := strings.TrimSuffix(ghost.Name, meta.NameSuffix)
		suffix := fc.offlineSuffix(userID)
		if base+suffix != ghost.Name {
			ghost.UpdateName(ctx, base+suffix)
			changed = true
		}
		if meta.NameSuffix != suffix {
			meta.NameSuffix = suffix
			changed = true
		}
	}
	if changed {
		if err = fc.Bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
			fc.Bridge.Log.Err(err).Str("ghost_id", string(userID)).Msg("Failed to save ghost presence")
		}
	}
	// Ghosts that stay offline don't need a presence update: after connecting, every ghost is
	// compared with the online list, and the homeserver resets the presence of ghosts anyway.
	if fc.Config.BridgePresence && (!offline || changed) {
		fc.setPresence(ctx, userID, update.status, update.statusMsg)
	}
}

// presenceRequest converts an F-Chat status to a Matrix presence.
func presenceRequest(status fchat.Status, statusMsg string) mautrix.ReqPresence {
	req := mautrix.ReqPresence{StatusMsg: statusMsg}
	label := ""
	switch status {
	case fchat.StatusOnline, fchat.StatusCrown:
		req.Presence = event.PresenceOnline
	case fchat.StatusLooking:
		req.Presence = event.PresenceOnline
		label = "Looking"
	case fchat.StatusBusy:
		req.Presence = event.PresenceUnavailable
		label = "Busy"
	case fchat.StatusDND:
		req.Presence = event.PresenceUnavailable
		label = "Do not disturb"
	case fchat.StatusIdle:
		req.Presence = event.PresenceUnavailable
		label = "Idle"
	case fchat.StatusAway:
		req.Presence = event.PresenceUnavailable
		label = "Away"
	default:
		req.Presence = event.PresenceOffline
		req.StatusMsg = ""
	}
	switch {
	case label != "" && statusMsg != "":
		req.StatusMsg = label + ": " + statusMsg
	case label != "":
		req.StatusMsg = label
	}
	return req
}

func (fc *FChatConnector) setPresence(ctx context.Context, userID networkid.UserID, status fchat.Status, statusMsg string) {
	mc := fc.matrixConnector()
	if mc == nil {
		return
	}
	mxid := fc.Bridge.Matrix.GhostIntent(userID).GetMXID()
	err := mc.AS.Intent(mxid).SetPresence(ctx, presenceRequest(status, statusMsg))
	if err != nil {
		fc.Bridge.Log.Debug().Err(err).Str("ghost_id", string(userID)).Msg("Failed to set presence")
	}
}

// presenceRefreshLoop sets the presence of online characters again and again: homeservers
// (like Synapse) reset the presence of users that don't sync after a short time.
func (fc *FChatConnector) presenceRefreshLoop() {
	interval := time.Duration(fc.Config.PresenceRefreshSeconds) * time.Second
	log := fc.Bridge.Log.With().Str("component", "presence").Logger()
	ctx := log.WithContext(context.Background())
	for {
		time.Sleep(interval)
		withDM, err := fc.ghostsWithDMs(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to get DM portals for presence refresh")
			continue
		}
		for userID, info := range fc.refreshTargets(withDM) {
			fc.setPresence(ctx, userID, info.Status, info.StatusMsg)
		}
	}
}

// refreshTargets returns the online characters whose presence is refreshed: the members of
// synced channels and the characters with a private chat.
func (fc *FChatConnector) refreshTargets(withDM map[networkid.UserID]bool) map[networkid.UserID]characterInfo {
	targets := make(map[networkid.UserID]characterInfo)
	for _, login := range fc.Bridge.GetAllCachedUserLogins() {
		client, ok := login.Client.(*FChatClient)
		if !ok || !client.Conn.IsConnected() {
			continue
		}
		client.stateLock.RLock()
		add := func(userID networkid.UserID) {
			if userID == client.ownID {
				return
			}
			if info, ok := client.characters[userID]; ok && !isOfflineStatus(info.Status) {
				targets[userID] = *info
			}
		}
		for _, ch := range client.channels {
			if !ch.Synced {
				continue
			}
			for userID := range ch.Members {
				add(userID)
			}
		}
		for userID := range withDM {
			add(userID)
		}
		client.stateLock.RUnlock()
	}
	return targets
}

// schedulePresenceSync compares all ghosts with the online list once the list is complete.
func (c *FChatClient) schedulePresenceSync() {
	c.presenceSyncLock.Lock()
	defer c.presenceSyncLock.Unlock()
	if c.presenceSyncTimer != nil {
		c.presenceSyncTimer.Reset(presenceSyncDelay)
		return
	}
	c.presenceSyncTimer = time.AfterFunc(presenceSyncDelay, func() {
		c.presenceSyncLock.Lock()
		c.presenceSyncTimer = nil
		c.presenceSyncLock.Unlock()
		c.syncGhostPresence()
	})
}

// syncGhostPresence queues a presence update for every ghost, for characters that came
// online or went offline while the bridge wasn't connected.
func (c *FChatClient) syncGhostPresence() {
	if !c.Conn.IsConnected() {
		return
	}
	ctx := c.ctx()
	ids, err := c.Main.allGhostIDs(ctx)
	if err != nil {
		c.log.Err(err).Msg("Failed to get ghosts for presence sync")
		return
	}
	for _, userID := range ids {
		if userID == c.ownID {
			continue
		}
		c.stateLock.RLock()
		info, ok := c.characters[userID]
		var status fchat.Status
		var statusMsg string
		if ok {
			status, statusMsg = info.Status, info.StatusMsg
		}
		c.stateLock.RUnlock()
		c.Main.queuePresence(userID, status, statusMsg)
	}
}

// checkRejoined removes a character that came online from the channel rooms it was in
// before going offline, if it didn't rejoin those channels.
func (c *FChatClient) checkRejoined(userID networkid.UserID, name string) {
	var left []string
	c.stateLock.Lock()
	info, online := c.characters[userID]
	if online && !isOfflineStatus(info.Status) {
		for chID, ch := range c.channels {
			if _, wasOffline := ch.Offline[userID]; !wasOffline {
				continue
			}
			delete(ch.Offline, userID)
			if _, member := ch.Members[userID]; !member {
				left = append(left, chID)
			}
		}
	}
	c.stateLock.Unlock()
	for _, channel := range left {
		c.queueMemberChange(channel, bridgev2.ChatMember{EventSender: c.sender(name), Membership: event.MembershipLeave})
	}
}
