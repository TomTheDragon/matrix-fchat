package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

const (
	powerLevelOwner = 75
	powerLevelOp    = 50
)

func (c *FChatClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	channel, dmUser, ok := parsePortalID(portal.ID)
	if !ok {
		return nil, fmt.Errorf("invalid portal ID %q", portal.ID)
	}
	if dmUser != "" {
		return c.dmChatInfo(dmUser), nil
	}
	return c.channelChatInfo(ctx, channel, portal), nil
}

func (c *FChatClient) dmChatInfo(other networkid.UserID) *bridgev2.ChatInfo {
	return &bridgev2.ChatInfo{
		Type: ptrTo(database.RoomTypeDM),
		Members: &bridgev2.ChatMemberList{
			IsFull:      true,
			OtherUserID: other,
			MemberMap: bridgev2.ChatMemberMap{}.
				Set(bridgev2.ChatMember{EventSender: c.sender(string(c.ownID)), Membership: event.MembershipJoin}).
				Set(bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{Sender: other},
					Membership:  event.MembershipJoin,
					UserInfo:    c.userInfoByID(other),
				}),
		},
	}
}

// userInfoByID returns the user info of a character. The name is only included
// if the properly capitalized form is known.
func (c *FChatClient) userInfoByID(userID networkid.UserID) *bridgev2.UserInfo {
	if name := c.characterName(userID); name != "" {
		return c.userInfo(name)
	}
	info := c.userInfo(string(userID))
	info.Name = nil
	return info
}

func (c *FChatClient) channelChatInfo(ctx context.Context, channel string, portal *bridgev2.Portal) *bridgev2.ChatInfo {
	c.stateLock.RLock()
	ch, known := c.channels[channel]
	var title, description string
	var members []bridgev2.ChatMember
	synced, initialized := false, false
	if known {
		initialized = ch.Initialized
		title = ch.Title
		description = ch.Description
		synced = ch.Synced && ch.Initialized
		if synced {
			members = make([]bridgev2.ChatMember, 0, len(ch.Members))
			for userID, name := range ch.Members {
				member := bridgev2.ChatMember{EventSender: c.sender(name), Membership: event.MembershipJoin}
				if userID != c.ownID {
					// Include the avatar: once a ghost has a name, bridgev2 doesn't ask for its info again.
					member.UserInfo = c.userInfo(name)
				}
				members = append(members, member)
			}
		}
	}
	c.stateLock.RUnlock()
	if synced && portal != nil && portal.MXID != "" {
		members = append(members, c.offlineMembers(ctx, channel, portal.MXID)...)
	}

	if title == "" {
		c.metaLock.Lock()
		title = c.meta().Channels[channel]
		c.metaLock.Unlock()
	}
	if title == "" && portal != nil {
		title = portal.Metadata.(*PortalMetadata).Title
	}
	if title == "" {
		title = channel
	}

	info := &bridgev2.ChatInfo{
		Name: ptrTo(title),
		Type: ptrTo(database.RoomTypeDefault),
		// Channels use the F-Chat icon, like the bot, the spaces and the management room.
		Avatar: &bridgev2.Avatar{
			ID:  networkid.AvatarID(NetworkIcon),
			MXC: NetworkIcon,
		},
		ExtraUpdates: func(ctx context.Context, portal *bridgev2.Portal) bool {
			meta := portal.Metadata.(*PortalMetadata)
			if meta.Title != title {
				meta.Title = title
				return true
			}
			return false
		},
	}
	if initialized {
		topic := bbcode.ToPlain(description)
		info.Topic = &topic
	}
	memberList := &bridgev2.ChatMemberList{
		IsFull:           synced,
		TotalMemberCount: len(members),
		PowerLevels:      c.powerLevelOverrides(channel),
	}
	if synced {
		memberList.Members = members
	} else {
		// Only make sure the user themselves is in the room.
		memberList.Members = []bridgev2.ChatMember{{EventSender: c.sender(string(c.ownID)), Membership: event.MembershipJoin}}
	}
	info.Members = memberList
	return info
}

// offlineMembers returns the ghosts in a channel room that went offline while in the channel,
// so that a full member sync (e.g. after reconnecting) doesn't remove them. Ghosts of online
// characters that aren't in the channel and of characters that were offline for longer than
// ghost_cleanup_days are left out, so they're removed.
func (c *FChatClient) offlineMembers(ctx context.Context, channel string, roomID id.RoomID) []bridgev2.ChatMember {
	current, err := c.Main.Bridge.Matrix.GetMembers(ctx, roomID)
	if err != nil {
		c.log.Warn().Err(err).Str("channel", channel).Msg("Failed to get room members to keep offline members")
		return nil
	}
	var candidates []networkid.UserID
	c.stateLock.RLock()
	ch, ok := c.channels[channel]
	if ok {
		for mxid, member := range current {
			if member.Membership != event.MembershipJoin {
				continue
			}
			userID, isGhost := c.Main.Bridge.Matrix.ParseGhostMXID(mxid)
			if !isGhost || userID == c.ownID {
				continue
			}
			if _, online := ch.Members[userID]; online {
				continue
			}
			if info, known := c.characters[userID]; known && !isOfflineStatus(info.Status) {
				// Online, but not in the channel anymore.
				continue
			}
			candidates = append(candidates, userID)
		}
	}
	c.stateLock.RUnlock()

	maxAge := time.Duration(c.Main.Config.GhostCleanupDays) * 24 * time.Hour
	kept := candidates[:0]
	for _, userID := range candidates {
		if maxAge <= 0 || !c.offlineTooLong(ctx, userID, maxAge) {
			kept = append(kept, userID)
		}
	}

	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	ch, ok = c.channels[channel]
	if !ok {
		return nil
	}
	offline := make(map[networkid.UserID]string, len(kept))
	members := make([]bridgev2.ChatMember, 0, len(kept))
	for _, userID := range kept {
		if _, online := ch.Members[userID]; online {
			continue
		}
		name := ch.Offline[userID]
		if name == "" {
			name = string(userID)
		}
		offline[userID] = name
		members = append(members, bridgev2.ChatMember{EventSender: c.sender(name), Membership: event.MembershipJoin})
	}
	ch.Offline = offline
	return members
}

// offlineTooLong returns whether a character was offline for longer than maxAge.
func (c *FChatClient) offlineTooLong(ctx context.Context, userID networkid.UserID, maxAge time.Duration) bool {
	ghost, err := c.Main.Bridge.GetExistingGhostByID(ctx, userID)
	if err != nil || ghost == nil {
		return false
	}
	meta, ok := ghost.Metadata.(*GhostMetadata)
	return ok && meta.OfflineSince != 0 && time.Since(time.Unix(meta.OfflineSince, 0)) > maxAge
}

// powerLevelOverrides maps F-Chat channel owners and operators to Matrix power levels.
func (c *FChatClient) powerLevelOverrides(channel string) *bridgev2.PowerLevelOverrides {
	c.stateLock.RLock()
	ch, ok := c.channels[channel]
	levels := make(map[networkid.UserID]int)
	if ok {
		for op := range ch.Ops {
			levels[op] = powerLevelOp
		}
		if ch.Owner != "" {
			levels[ch.Owner] = powerLevelOwner
		}
	}
	c.stateLock.RUnlock()
	if !ok {
		return nil
	}
	matrix := c.Main.Bridge.Matrix
	return &bridgev2.PowerLevelOverrides{
		Events: map[event.Type]int{
			event.StateTopic: powerLevelOp,
		},
		Custom: func(content *event.PowerLevelsEventContent) bool {
			changed := false
			// Demote ghosts that are no longer operators.
			for userID, level := range content.Users {
				ghostID, isGhost := matrix.ParseGhostMXID(userID)
				if !isGhost || ghostID == c.ownID {
					continue
				}
				if _, stillOp := levels[ghostID]; !stillOp && (level == powerLevelOp || level == powerLevelOwner) {
					content.SetUserLevel(userID, 0)
					changed = true
				}
			}
			for userID, level := range levels {
				if userID == c.ownID {
					continue
				}
				mxid := matrix.FormatGhostMXID(userID)
				if content.GetUserLevel(mxid) != level {
					content.SetUserLevel(mxid, level)
					changed = true
				}
			}
			return changed
		},
	}
}

func (c *FChatClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	name := c.characterName(ghost.ID)
	if name == "" {
		if ghost.Name != "" {
			name = ghost.Name
		} else {
			name = string(ghost.ID)
		}
	}
	return c.userInfo(name), nil
}

// avatarRefreshPeriod is how often avatars are downloaded again. F-List avatar URLs
// don't change when the image does, so the avatar ID includes the current period.
// The image is only uploaded again if it actually changed.
const avatarRefreshPeriod = 7 * 24 * time.Hour

func avatarID(avatarURL string, now time.Time) networkid.AvatarID {
	return networkid.AvatarID(fmt.Sprintf("%s#%d", avatarURL, now.Unix()/int64(avatarRefreshPeriod/time.Second)))
}

func (fc *FChatConnector) makeUserInfo(name string) *bridgev2.UserInfo {
	avatarURL := fchat.AvatarURL(fc.Config.StaticURL, name)
	info := &bridgev2.UserInfo{
		Name: ptrTo(name + fc.offlineSuffix(makeUserID(name))),
		Avatar: &bridgev2.Avatar{
			ID: avatarID(avatarURL, time.Now()),
			Get: func(ctx context.Context) ([]byte, error) {
				return fc.download(ctx, avatarURL)
			},
		},
	}
	if fc.Config.BeeperProfileFields {
		// Only used for the com.beeper.bridge.* profile fields.
		info.Identifiers = []string{"fchat:" + name, fchat.ProfileURL(fc.Config.SiteURL, name)}
		info.IsBot = ptrTo(false)
	}
	return info
}

func (fc *FChatConnector) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fc.userAgent())
	resp, err := fc.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d downloading %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
}

func ptrTo[T any](v T) *T {
	return &v
}

func (c *FChatClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	channel, _, _ := parsePortalID(portal.ID)
	maxLength := c.Conn.PrivMax()
	if channel != "" {
		maxLength = c.Conn.ChatMax()
	}
	fileFeatures := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelPartialSupport},
		Caption:   event.CapLevelPartialSupport,
	}
	files := event.FileFeatureMap{}
	if _, ok := c.Main.Bridge.Matrix.(bridgev2.MatrixConnectorWithPublicMedia); ok {
		// Files are sent as links when public media is enabled.
		for _, msgType := range []event.CapabilityMsgType{event.MsgImage, event.MsgFile, event.MsgVideo, event.MsgAudio, event.CapMsgGIF} {
			files[msgType] = fileFeatures
		}
	}
	return &event.RoomFeatures{
		ID: "fi.mau.fchat.capabilities.2026_10_25",
		Formatting: event.FormattingFeatureMap{
			event.FmtBold:                event.CapLevelFullySupported,
			event.FmtItalic:              event.CapLevelFullySupported,
			event.FmtUnderline:           event.CapLevelFullySupported,
			event.FmtStrikethrough:       event.CapLevelFullySupported,
			event.FmtSuperscript:         event.CapLevelFullySupported,
			event.FmtSubscript:           event.CapLevelFullySupported,
			event.FmtInlineLink:          event.CapLevelFullySupported,
			event.FmtSpoiler:             event.CapLevelFullySupported,
			event.FmtTextForegroundColor: event.CapLevelPartialSupport,
			event.FmtUserLink:            event.CapLevelPartialSupport,
			event.FmtInlineCode:          event.CapLevelPartialSupport,
			event.FmtCodeBlock:           event.CapLevelPartialSupport,
			event.FmtBlockquote:          event.CapLevelPartialSupport,
			event.FmtUnorderedList:       event.CapLevelPartialSupport,
			event.FmtOrderedList:         event.CapLevelPartialSupport,
			event.FmtHeaders:             event.CapLevelPartialSupport,
		},
		File:                files,
		MaxTextLength:       maxLength,
		LocationMessage:     event.CapLevelRejected,
		Poll:                event.CapLevelRejected,
		Reply:               event.CapLevelDropped,
		Thread:              event.CapLevelDropped,
		Edit:                event.CapLevelRejected,
		Delete:              event.CapLevelRejected,
		Reaction:            event.CapLevelRejected,
		TypingNotifications: channel == "",
	}
}
