package connector

import (
	"context"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// The ghost cleanup removes ghosts that weren't seen for ghost_cleanup_days and that nobody
// has a private chat with. bridgev2 can't delete ghosts and an appservice can't delete Matrix
// accounts, so "removing" means: the ghost leaves all rooms, its Matrix profile is cleared and
// it's marked as removed. If the character shows up again, the profile is set again.

const (
	ghostCleanupInterval   = time.Hour
	ghostCleanupStartDelay = 10 * time.Minute
)

func (fc *FChatConnector) startGhostCleanup() {
	if fc.Config.GhostCleanupDays <= 0 {
		return
	}
	go func() {
		time.Sleep(ghostCleanupStartDelay)
		for {
			fc.cleanupGhosts(fc.Bridge.BackgroundCtx)
			time.Sleep(ghostCleanupInterval)
		}
	}()
}

// activeCharacters returns the characters that must not be removed: the logged-in characters
// and the members of joined channels. It returns false if a login isn't connected, because the
// channel member lists are only known while connected.
func (fc *FChatConnector) activeCharacters() (map[networkid.UserID]bool, bool) {
	active := make(map[networkid.UserID]bool)
	for _, login := range fc.Bridge.GetAllCachedUserLogins() {
		client, ok := login.Client.(*FChatClient)
		if !ok {
			continue
		}
		if !client.Conn.IsConnected() {
			return nil, false
		}
		active[client.ownID] = true
		client.stateLock.RLock()
		for _, ch := range client.channels {
			for userID := range ch.Members {
				active[userID] = true
			}
		}
		client.stateLock.RUnlock()
	}
	return active, true
}

func (fc *FChatConnector) cleanupGhosts(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	log := fc.Bridge.Log.With().Str("action", "ghost cleanup").Logger()
	ctx = log.WithContext(ctx)
	maxAge := time.Duration(fc.Config.GhostCleanupDays) * 24 * time.Hour

	active, ok := fc.activeCharacters()
	if !ok {
		log.Debug().Msg("Skipping ghost cleanup, not all logins are connected")
		return
	}
	withDM, err := fc.ghostsWithDMs(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get DM portals")
		return
	}
	ids, err := fc.allGhostIDs(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get ghosts")
		return
	}
	removed := 0
	for _, userID := range ids {
		if ctx.Err() != nil {
			return
		}
		if active[userID] || withDM[userID] {
			continue
		}
		ghost, err := fc.Bridge.GetExistingGhostByID(ctx, userID)
		if err != nil || ghost == nil {
			continue
		}
		meta, ok := ghost.Metadata.(*GhostMetadata)
		if !ok || meta.Removed {
			continue
		}
		if meta.LastSeen == 0 {
			// Ghosts from before the cleanup existed: start counting now.
			meta.LastSeen = time.Now().Unix()
			if err = fc.Bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
				log.Err(err).Str("ghost_id", string(userID)).Msg("Failed to save ghost")
			}
			continue
		}
		if time.Since(time.Unix(meta.LastSeen, 0)) < maxAge {
			continue
		}
		fc.retireGhost(ctx, ghost)
		removed++
	}
	if removed > 0 {
		log.Info().Int("count", removed).Msg("Removed unused ghosts")
	}
}

// allGhostIDs returns the IDs of all ghosts in the database.
func (fc *FChatConnector) allGhostIDs(ctx context.Context) ([]networkid.UserID, error) {
	rows, err := fc.Bridge.DB.Query(ctx, "SELECT id FROM ghost WHERE bridge_id=$1", fc.Bridge.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []networkid.UserID
	for rows.Next() {
		var userID networkid.UserID
		if err = rows.Scan(&userID); err != nil {
			return nil, err
		}
		if userID != "" {
			ids = append(ids, userID)
		}
	}
	return ids, rows.Err()
}

// ghostsWithDMs returns the characters that have a DM portal with any login.
func (fc *FChatConnector) ghostsWithDMs(ctx context.Context) (map[networkid.UserID]bool, error) {
	rows, err := fc.Bridge.DB.Query(ctx, "SELECT id FROM portal WHERE bridge_id=$1 AND id LIKE $2", fc.Bridge.ID, dmPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	withDM := make(map[networkid.UserID]bool)
	for rows.Next() {
		var portalID string
		if err = rows.Scan(&portalID); err != nil {
			return nil, err
		}
		withDM[networkid.UserID(strings.TrimPrefix(portalID, dmPrefix))] = true
	}
	return withDM, rows.Err()
}

// retireGhost makes a ghost leave all rooms and clears its Matrix profile.
func (fc *FChatConnector) retireGhost(ctx context.Context, ghost *bridgev2.Ghost) {
	log := fc.Bridge.Log.With().Str("ghost_id", string(ghost.ID)).Logger()
	mxid := ghost.Intent.GetMXID()
	if mc := fc.matrixConnector(); mc != nil {
		intent := mc.AS.Intent(mxid)
		rooms, err := intent.JoinedRooms(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get joined rooms of ghost")
		} else {
			for _, roomID := range rooms.JoinedRooms {
				if _, err = intent.LeaveRoom(ctx, roomID); err != nil {
					log.Warn().Err(err).Stringer("room_id", roomID).Msg("Failed to leave room")
				}
			}
		}
	}
	if err := ghost.Intent.SetDisplayName(ctx, ""); err != nil {
		log.Warn().Err(err).Msg("Failed to clear display name")
	}
	if err := ghost.Intent.SetAvatarURL(ctx, id.ContentURIString("")); err != nil {
		log.Warn().Err(err).Msg("Failed to clear avatar")
	}
	if fc.extraProfileSupported() {
		cleared := make(database.ExtraProfile)
		for _, key := range allProfileKeys {
			_ = cleared.Set(key, nil)
		}
		fc.setProfileFields(ctx, ghost, cleared)
	}
	// Reset the ghost, so name and avatar are set again if the character comes back.
	ghost.Name = ""
	ghost.NameSet = false
	ghost.AvatarID = ""
	ghost.AvatarMXC = ""
	ghost.AvatarHash = [32]byte{}
	ghost.AvatarSet = false
	ghost.ContactInfoSet = false
	meta := ghost.Metadata.(*GhostMetadata)
	meta.Removed = true
	meta.ProfileFetched = 0
	meta.ProfileFieldsHash = ""
	if err := fc.Bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
		log.Err(err).Msg("Failed to save removed ghost")
	}
	log.Debug().Msg("Removed unused ghost")
}
