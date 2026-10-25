package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

func parsePayload[T any](c *FChatClient, cmd fchat.RawCommand) (*T, bool) {
	var out T
	if len(cmd.Payload) == 0 {
		c.log.Warn().Str("code", cmd.Code).Msg("Command is missing payload")
		return nil, false
	}
	if err := json.Unmarshal(cmd.Payload, &out); err != nil {
		c.log.Warn().Err(err).Str("code", cmd.Code).Msg("Failed to parse command payload")
		return nil, false
	}
	return &out, true
}

func (c *FChatClient) handleCommand(cmd fchat.RawCommand) {
	switch cmd.Code {
	case fchat.CmdMessage:
		if msg, ok := parsePayload[fchat.ChannelMessage](c, cmd); ok {
			c.handleChannelMessage(msg, false)
		}
	case fchat.CmdAd:
		if msg, ok := parsePayload[fchat.ChannelMessage](c, cmd); ok && c.Main.Config.BridgeAds {
			c.handleChannelMessage(msg, true)
		}
	case fchat.CmdPrivate:
		if msg, ok := parsePayload[fchat.PrivateMessage](c, cmd); ok {
			c.handlePrivateMessage(msg)
		}
	case fchat.CmdRoll:
		if roll, ok := parsePayload[fchat.Roll](c, cmd); ok {
			c.handleRoll(roll)
		}
	case fchat.CmdTyping:
		if typing, ok := parsePayload[fchat.TypingRequest](c, cmd); ok {
			c.handleTyping(typing)
		}
	case fchat.CmdJoinChannel:
		if join, ok := parsePayload[fchat.JoinChannel](c, cmd); ok {
			c.handleJoin(join)
		}
	case fchat.CmdLeaveChannel:
		if leave, ok := parsePayload[fchat.LeaveChannel](c, cmd); ok {
			c.handleLeave(leave.Channel, leave.Character, "", event.MembershipLeave, "")
		}
	case fchat.CmdInitialChannel:
		if ich, ok := parsePayload[fchat.InitialChannelData](c, cmd); ok {
			c.handleInitialChannelData(ich)
		}
	case fchat.CmdChannelDesc:
		if cds, ok := parsePayload[fchat.DescriptionChange](c, cmd); ok {
			c.handleDescription(cds)
		}
	case fchat.CmdOpList:
		if col, ok := parsePayload[fchat.OpList](c, cmd); ok {
			c.handleOpList(col)
		}
	case fchat.CmdOpAdd, fchat.CmdOpRemove:
		if change, ok := parsePayload[fchat.OpChange](c, cmd); ok {
			c.handleOpChange(change, cmd.Code == fchat.CmdOpAdd)
		}
	case fchat.CmdSetOwner:
		if change, ok := parsePayload[fchat.OpChange](c, cmd); ok {
			c.handleOwnerChange(change)
		}
	case fchat.CmdChannelKick:
		if mod, ok := parsePayload[fchat.ChannelModeration](c, cmd); ok {
			c.handleLeave(mod.Channel, mod.Character, mod.Operator, event.MembershipLeave, "Kicked from the channel")
		}
	case fchat.CmdChannelBan:
		if mod, ok := parsePayload[fchat.ChannelModeration](c, cmd); ok {
			c.handleLeave(mod.Channel, mod.Character, mod.Operator, event.MembershipBan, "Banned from the channel")
		}
	case fchat.CmdChannelTimeout:
		if mod, ok := parsePayload[fchat.ChannelModeration](c, cmd); ok {
			c.handleLeave(mod.Channel, mod.Character, mod.Operator, event.MembershipLeave,
				fmt.Sprintf("Timed out from the channel for %d minute(s)", mod.Length))
		}
	case fchat.CmdRoomMode:
		if rmo, ok := parsePayload[fchat.RoomModeChange](c, cmd); ok {
			c.handleRoomMode(rmo)
		}
	case fchat.CmdOnline:
		if nln, ok := parsePayload[fchat.OnlineNotice](c, cmd); ok {
			c.handleOnline(nln)
		}
	case fchat.CmdOffline:
		if fln, ok := parsePayload[fchat.OfflineNotice](c, cmd); ok {
			c.handleOffline(fln.Character)
		}
	case fchat.CmdOnlineList:
		if lis, ok := parsePayload[fchat.OnlineList](c, cmd); ok {
			c.handleOnlineList(lis)
		}
	case fchat.CmdStatus:
		if sta, ok := parsePayload[fchat.StatusChange](c, cmd); ok {
			c.handleStatus(sta)
		}
	case fchat.CmdFriendList:
		if frl, ok := parsePayload[fchat.FriendList](c, cmd); ok {
			c.handleFriendList(frl)
		}
	case fchat.CmdIgnore:
		if ign, ok := parsePayload[fchat.IgnoreList](c, cmd); ok {
			c.handleIgnore(ign)
		}
	case fchat.CmdChannelInvite:
		if ciu, ok := parsePayload[fchat.ChannelInvite](c, cmd); ok {
			c.handleInvite(ciu)
		}
	case fchat.CmdSystem:
		if sys, ok := parsePayload[fchat.SystemMessage](c, cmd); ok {
			c.handleSystemMessage(sys)
		}
	case fchat.CmdBroadcast:
		if bro, ok := parsePayload[fchat.Broadcast](c, cmd); ok {
			c.sendManagementNotice(c.ctx(), "**F-Chat broadcast:** "+bbcode.ToPlain(bbcode.DecodeEntities(bro.Message)))
		}
	case fchat.CmdRealtimeBridge:
		if rtb, ok := parsePayload[fchat.RealtimeBridge](c, cmd); ok {
			c.handleRealtimeBridge(rtb)
		}
	case fchat.CmdError:
		if fcErr, ok := parsePayload[fchat.Error](c, cmd); ok {
			c.handleUnsolicitedError(fcErr)
		}
	case fchat.CmdHello, fchat.CmdConnectedCount, fchat.CmdAdminList, fchat.CmdVariable, fchat.CmdUptime,
		"AOP", "DOP", fchat.CmdPublicChannels, fchat.CmdOpenRooms, "PRD", "KID", "FKS", "SFC":
		// Informational or handled elsewhere.
	default:
		c.log.Debug().Str("code", cmd.Code).Msg("Ignoring unknown command")
	}
}

func (c *FChatClient) bbcodeOptions() bbcode.Options {
	ownName := c.meta().Character
	return bbcode.Options{
		ProfileURL: func(name string) string {
			return fchat.ProfileURL(c.Main.Config.SiteURL, name)
		},
		EIconURL: func(name string) string {
			return fchat.EIconURL(c.Main.Config.StaticURL, name)
		},
		UserMention: func(name string) string {
			if strings.EqualFold(name, ownName) {
				return string(c.UserLogin.UserMXID)
			}
			return string(c.Main.Bridge.Matrix.FormatGhostMXID(makeUserID(name)))
		},
	}
}

// convertText converts an F-Chat message text into Matrix message content.
func (c *FChatClient) convertText(text string, msgType event.MessageType, prefix string) *event.MessageEventContent {
	text = bbcode.DecodeEntities(text)
	if msgType == event.MsgText {
		// "/me" actions are sent as normal messages with a prefix.
		if rest, ok := strings.CutPrefix(text, "/me "); ok {
			msgType = event.MsgEmote
			text = rest
		} else if strings.HasPrefix(text, "/me's ") {
			msgType = event.MsgEmote
			text = text[3:]
		}
	}
	res := bbcode.ToHTML(text, c.bbcodeOptions())
	content := &event.MessageEventContent{
		MsgType: msgType,
		Body:    prefix + res.Plain,
	}
	if res.HasFormatting || prefix != "" {
		content.Format = event.FormatHTML
		htmlPrefix := ""
		if prefix != "" {
			htmlPrefix = "<strong>" + strings.TrimSpace(prefix) + "</strong> "
		}
		content.FormattedBody = htmlPrefix + res.HTML
	}
	// Mention the Matrix user if their character is mentioned in a [user] tag.
	ownMXID := c.UserLogin.UserMXID
	if strings.Contains(content.FormattedBody, "https://matrix.to/#/"+string(ownMXID)) {
		content.Mentions = &event.Mentions{UserIDs: []id.UserID{ownMXID}}
	} else {
		content.Mentions = &event.Mentions{}
	}
	return content
}

func (c *FChatClient) queueMessage(portalKey networkid.PortalKey, senderName string, content *event.MessageEventContent, createPortal bool) {
	sender := c.sender(senderName)
	msgID := makeMessageID(sender.Sender)
	c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.PreConvertedMessage{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    portalKey,
			Sender:       sender,
			CreatePortal: createPortal,
			Timestamp:    time.Now(),
			LogContext: func(lc zerolog.Context) zerolog.Context {
				return lc.Str("message_id", string(msgID)).Str("sender", senderName)
			},
		},
		ID: msgID,
		Data: &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{{
				Type:    event.EventMessage,
				Content: content,
			}},
		},
	})
}

func (c *FChatClient) isInChannel(channel string) bool {
	c.stateLock.RLock()
	_, ok := c.channels[channel]
	c.stateLock.RUnlock()
	if ok {
		return true
	}
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	_, ok = c.meta().Channels[channel]
	return ok
}

func (c *FChatClient) handleChannelMessage(msg *fchat.ChannelMessage, isAd bool) {
	if c.isIgnored(msg.Character) {
		return
	}
	if !c.isInChannel(msg.Channel) {
		// The server thinks we're in a channel we don't know about, leave it.
		c.log.Debug().Str("channel", msg.Channel).Msg("Got message from unknown channel, leaving")
		_ = c.Conn.Send(c.ctx(), fchat.CmdLeaveChannel, &fchat.ChannelRequest{Channel: msg.Channel})
		return
	}
	c.rememberName(msg.Character)
	prefix := ""
	if isAd {
		prefix = "[Ad] "
	}
	content := c.convertText(msg.Message, event.MsgText, prefix)
	c.queueMessage(makeChannelPortalKey(msg.Channel, c.UserLogin.ID), msg.Character, content, false)
}

func (c *FChatClient) handlePrivateMessage(msg *fchat.PrivateMessage) {
	if c.isIgnored(msg.Character) {
		// The protocol expects clients to tell the server when they drop a PM.
		_ = c.Conn.Send(c.ctx(), fchat.CmdIgnore, &fchat.IgnoreRequest{Action: "notify", Character: msg.Character})
		return
	}
	c.rememberName(msg.Character)
	content := c.convertText(msg.Message, event.MsgText, "")
	c.queueMessage(makeDMPortalKey(msg.Character, c.UserLogin.ID), msg.Character, content, true)
}

func (c *FChatClient) handleRoll(roll *fchat.Roll) {
	if c.isIgnored(roll.Character) {
		return
	}
	var portalKey networkid.PortalKey
	createPortal := false
	switch {
	case roll.Channel != "":
		if !c.isInChannel(roll.Channel) {
			return
		}
		portalKey = makeChannelPortalKey(roll.Channel, c.UserLogin.ID)
	case roll.Recipient != "":
		// Rolls in private messages: the portal is the DM with the other party.
		other := roll.Recipient
		if makeUserID(other) == c.ownID {
			other = roll.Character
		}
		portalKey = makeDMPortalKey(other, c.UserLogin.ID)
		createPortal = true
	default:
		return
	}
	c.rememberName(roll.Character)
	content := c.convertText(roll.Message, event.MsgNotice, "🎲 ")
	c.queueMessage(portalKey, roll.Character, content, createPortal)
}

func (c *FChatClient) handleTyping(typing *fchat.TypingRequest) {
	if c.isIgnored(typing.Character) {
		return
	}
	timeout := time.Duration(0)
	if typing.Status == fchat.TypingActive {
		timeout = 15 * time.Second
	}
	c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: makeDMPortalKey(typing.Character, c.UserLogin.ID),
			Sender:    c.sender(typing.Character),
		},
		Timeout: timeout,
		Type:    bridgev2.TypingTypeText,
	})
}

func (c *FChatClient) saveChannelMeta(channel, title string, joined bool) {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	meta := c.meta()
	if joined {
		if meta.Channels == nil {
			meta.Channels = make(map[string]string)
		}
		if existing, ok := meta.Channels[channel]; ok && existing == title {
			return
		}
		meta.Channels[channel] = title
	} else {
		if _, ok := meta.Channels[channel]; !ok {
			return
		}
		delete(meta.Channels, channel)
	}
	// Save while holding the lock so the map isn't modified during serialization.
	if err := c.UserLogin.Save(c.ctx()); err != nil {
		c.log.Err(err).Msg("Failed to save joined channel list")
	}
}

func (c *FChatClient) handleJoin(join *fchat.JoinChannel) {
	name := join.Character.Identity
	userID := makeUserID(name)
	title := bbcode.DecodeEntities(join.Title)
	if userID == c.ownID {
		c.stateLock.Lock()
		if _, ok := c.channels[join.Channel]; !ok {
			c.channels[join.Channel] = &channelState{
				ID:      join.Channel,
				Title:   title,
				Members: map[networkid.UserID]string{c.ownID: name},
				Ops:     make(map[networkid.UserID]bool),
			}
		} else if title != "" {
			c.channels[join.Channel].Title = title
		}
		c.stateLock.Unlock()
		c.saveChannelMeta(join.Channel, title, true)
		// The portal is created when the member list (ICH) arrives.
		return
	}
	c.stateLock.Lock()
	ch, ok := c.channels[join.Channel]
	if !ok {
		c.stateLock.Unlock()
		return
	}
	_, alreadyMember := ch.Members[userID]
	ch.Members[userID] = name
	synced := ch.Synced && ch.Initialized
	c.stateLock.Unlock()
	if alreadyMember || !synced {
		return
	}
	c.queueMemberChange(join.Channel, bridgev2.ChatMember{
		EventSender: c.sender(name),
		Membership:  event.MembershipJoin,
	})
}

func (c *FChatClient) queueMemberChange(channel string, members ...bridgev2.ChatMember) {
	memberMap := make(bridgev2.ChatMemberMap, len(members))
	for _, member := range members {
		memberMap.Set(member)
	}
	c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: makeChannelPortalKey(channel, c.UserLogin.ID),
			Sender:    members[0].MemberSender,
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{MemberMap: memberMap},
		},
	})
}

func (c *FChatClient) handleLeave(channel, character, operator string, membership event.Membership, reason string) {
	userID := makeUserID(character)
	if userID == c.ownID {
		c.stateLock.Lock()
		_, wasKnown := c.channels[channel]
		delete(c.channels, channel)
		c.stateLock.Unlock()
		c.saveChannelMeta(channel, "", false)
		if !wasKnown && operator == "" {
			return
		}
		member := bridgev2.ChatMember{
			EventSender: c.sender(character),
			Membership:  membership,
		}
		if operator != "" {
			member.MemberSender = c.sender(operator)
			member.MemberEventExtra = map[string]any{"reason": reason}
			c.sendPortalNotice(channel, fmt.Sprintf("%s by %s", reason, operator))
		}
		c.queueMemberChange(channel, member)
		return
	}
	c.stateLock.Lock()
	ch, ok := c.channels[channel]
	if !ok {
		c.stateLock.Unlock()
		return
	}
	_, wasMember := ch.Members[userID]
	delete(ch.Members, userID)
	synced := ch.Synced
	c.stateLock.Unlock()
	if !synced || (!wasMember && operator == "") {
		return
	}
	member := bridgev2.ChatMember{
		EventSender: c.sender(character),
		Membership:  membership,
	}
	if operator != "" {
		member.MemberSender = c.sender(operator)
		member.MemberEventExtra = map[string]any{"reason": reason}
	}
	c.queueMemberChange(channel, member)
}

func (c *FChatClient) handleInitialChannelData(ich *fchat.InitialChannelData) {
	c.stateLock.Lock()
	ch, ok := c.channels[ich.Channel]
	if !ok {
		// ICH without JCH shouldn't happen, but handle it like an unsolicited join.
		ch = &channelState{ID: ich.Channel, Title: ich.Channel, Ops: make(map[networkid.UserID]bool)}
		c.channels[ich.Channel] = ch
	}
	ch.Mode = ich.Mode
	ch.Members = make(map[networkid.UserID]string, len(ich.Users))
	for _, user := range ich.Users {
		ch.Members[makeUserID(user.Identity)] = user.Identity
	}
	ch.Members[c.ownID] = c.meta().Character
	ch.Synced = c.Main.Config.ShouldSyncMembers(len(ch.Members))
	ch.Initialized = true
	title := ch.Title
	c.stateLock.Unlock()
	if !ok {
		c.saveChannelMeta(ich.Channel, title, true)
	}

	channel := ich.Channel
	c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    makeChannelPortalKey(channel, c.UserLogin.ID),
			CreatePortal: true,
			LogContext: func(lc zerolog.Context) zerolog.Context {
				return lc.Str("channel", channel)
			},
		},
		GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
			return c.GetChatInfo(ctx, portal)
		},
	})
}

func (c *FChatClient) handleDescription(cds *fchat.DescriptionChange) {
	description := bbcode.DecodeEntities(cds.Description)
	c.stateLock.Lock()
	ch, ok := c.channels[cds.Channel]
	changed := ok && ch.Description != description
	if ok {
		ch.Description = description
	}
	initialized := ok && ch.Initialized
	c.stateLock.Unlock()
	if !changed || !initialized {
		// Before ICH, the description is included in the initial chat info.
		return
	}
	topic := bbcode.ToPlain(description)
	c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: makeChannelPortalKey(cds.Channel, c.UserLogin.ID),
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{Topic: &topic},
		},
	})
}

func (c *FChatClient) queuePowerLevelUpdate(channel string) {
	c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: makeChannelPortalKey(channel, c.UserLogin.ID),
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				Members: &bridgev2.ChatMemberList{PowerLevels: c.powerLevelOverrides(channel)},
			},
		},
	})
}

func (c *FChatClient) handleOpList(col *fchat.OpList) {
	c.stateLock.Lock()
	ch, ok := c.channels[col.Channel]
	if ok {
		ch.Ops = make(map[networkid.UserID]bool, len(col.OpList))
		ch.Owner = ""
		for i, op := range col.OpList {
			if op == "" {
				continue
			}
			if i == 0 {
				// The first entry is the channel owner (or "" for official channels without an owner).
				ch.Owner = makeUserID(op)
			}
			ch.Ops[makeUserID(op)] = true
		}
	}
	initialized := ok && ch.Initialized
	c.stateLock.Unlock()
	if initialized {
		c.queuePowerLevelUpdate(col.Channel)
	}
}

func (c *FChatClient) handleOpChange(change *fchat.OpChange, added bool) {
	c.stateLock.Lock()
	ch, ok := c.channels[change.Channel]
	if ok {
		if added {
			ch.Ops[makeUserID(change.Character)] = true
		} else {
			delete(ch.Ops, makeUserID(change.Character))
		}
	}
	c.stateLock.Unlock()
	if ok {
		c.queuePowerLevelUpdate(change.Channel)
	}
}

func (c *FChatClient) handleOwnerChange(change *fchat.OpChange) {
	c.stateLock.Lock()
	ch, ok := c.channels[change.Channel]
	if ok {
		ch.Owner = makeUserID(change.Character)
		ch.Ops[ch.Owner] = true
	}
	c.stateLock.Unlock()
	if ok {
		c.queuePowerLevelUpdate(change.Channel)
	}
}

func (c *FChatClient) handleRoomMode(rmo *fchat.RoomModeChange) {
	c.stateLock.Lock()
	ch, ok := c.channels[rmo.Channel]
	if ok {
		ch.Mode = rmo.Mode
	}
	c.stateLock.Unlock()
	if !ok {
		return
	}
	var desc string
	switch rmo.Mode {
	case fchat.ModeChat:
		desc = "only chat messages are allowed"
	case fchat.ModeAds:
		desc = "only roleplay ads are allowed"
	default:
		desc = "chat messages and roleplay ads are allowed"
	}
	c.sendPortalNotice(rmo.Channel, "Channel mode changed: "+desc)
}

func (c *FChatClient) handleOnline(nln *fchat.OnlineNotice) {
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	c.characters[makeUserID(nln.Identity)] = &characterInfo{
		Name:   nln.Identity,
		Gender: nln.Gender,
		Status: nln.Status,
	}
}

func (c *FChatClient) handleOnlineList(lis *fchat.OnlineList) {
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	for _, entry := range lis.Characters {
		if len(entry) == 0 {
			continue
		}
		info := &characterInfo{Name: entry[0]}
		if len(entry) > 1 {
			info.Gender = entry[1]
		}
		if len(entry) > 2 {
			info.Status = fchat.Status(entry[2])
		}
		if len(entry) > 3 {
			info.StatusMsg = entry[3]
		}
		c.characters[makeUserID(info.Name)] = info
	}
}

func (c *FChatClient) handleOffline(character string) {
	userID := makeUserID(character)
	var leftChannels []string
	c.stateLock.Lock()
	if info, ok := c.characters[userID]; ok {
		info.Status = fchat.StatusOffline
		info.StatusMsg = ""
	}
	for chID, ch := range c.channels {
		if _, ok := ch.Members[userID]; ok {
			delete(ch.Members, userID)
			if ch.Synced {
				leftChannels = append(leftChannels, chID)
			}
		}
	}
	c.stateLock.Unlock()
	// FLN is a global leave for all channels the character was in.
	for _, channel := range leftChannels {
		c.queueMemberChange(channel, bridgev2.ChatMember{
			EventSender: c.sender(character),
			Membership:  event.MembershipLeave,
		})
	}
}

func (c *FChatClient) handleStatus(sta *fchat.StatusChange) {
	userID := makeUserID(sta.Character)
	statusMsg := bbcode.DecodeEntities(sta.StatusMsg)
	c.stateLock.Lock()
	info, ok := c.characters[userID]
	if !ok {
		info = &characterInfo{Name: sta.Character}
		c.characters[userID] = info
	}
	info.Status = sta.Status
	info.StatusMsg = statusMsg
	c.stateLock.Unlock()
	if !c.Main.Config.BridgeStatusChanges || userID == c.ownID {
		return
	}
	ctx := c.ctx()
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, makeDMPortalKey(sta.Character, c.UserLogin.ID))
	if err != nil || portal == nil || portal.MXID == "" {
		return
	}
	text := fmt.Sprintf("is now %s", sta.Status)
	if statusMsg != "" {
		text += ": " + statusMsg
	}
	content := c.convertText(text, event.MsgNotice, "")
	content.Mentions = &event.Mentions{}
	c.queueMessage(portal.PortalKey, sta.Character, content, false)
}

func (c *FChatClient) handleFriendList(frl *fchat.FriendList) {
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	clear(c.friends)
	for _, name := range frl.Characters {
		c.friends[makeUserID(name)] = name
	}
}

func (c *FChatClient) handleIgnore(ign *fchat.IgnoreList) {
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	switch ign.Action {
	case "init", "list":
		clear(c.ignored)
		for _, name := range ign.Characters {
			c.ignored[makeUserID(name)] = true
		}
	case "add":
		c.ignored[makeUserID(ign.Character)] = true
	case "delete":
		delete(c.ignored, makeUserID(ign.Character))
	}
}

func (c *FChatClient) handleInvite(ciu *fchat.ChannelInvite) {
	if c.isIgnored(ciu.Sender) {
		return
	}
	title := bbcode.DecodeEntities(ciu.Title)
	c.sendManagementNotice(c.ctx(), fmt.Sprintf(
		"**%s** invited you to the channel **%s**. Join it with `$cmdprefix join %s`",
		ciu.Sender, title, ciu.Name,
	))
}

func (c *FChatClient) handleSystemMessage(sys *fchat.SystemMessage) {
	text := bbcode.ToPlain(bbcode.DecodeEntities(sys.Message))
	if sys.Channel != "" && c.isInChannel(sys.Channel) {
		c.sendPortalNotice(sys.Channel, text)
		return
	}
	c.sendManagementNotice(c.ctx(), "**F-Chat:** "+text)
}

func (c *FChatClient) handleRealtimeBridge(rtb *fchat.RealtimeBridge) {
	var text string
	switch rtb.Type {
	case "note":
		text = fmt.Sprintf("New note from **%s**: %s", rtb.Sender, rtb.Subject)
	case "comment":
		text = fmt.Sprintf("New comment from **%s**", rtb.Character)
	case "friendrequest":
		text = fmt.Sprintf("New friend request from **%s**", rtb.Name)
	case "friendadd":
		text = fmt.Sprintf("**%s** is now your friend", rtb.Name)
	case "friendremove":
		text = fmt.Sprintf("**%s** is no longer your friend", rtb.Name)
	default:
		text = fmt.Sprintf("F-List notification: %s", rtb.Type)
	}
	c.sendManagementNotice(c.ctx(), text)
}

func (c *FChatClient) handleUnsolicitedError(fcErr *fchat.Error) {
	c.log.Warn().Int("code", fcErr.Code).Str("message", fcErr.Message).Msg("Received error from F-Chat")
	if fcErr.Code == fchat.ErrCodeAlreadyInChannel && c.rejoining.Load() {
		return
	}
	c.sendManagementNotice(c.ctx(), fmt.Sprintf("**F-Chat error %d:** %s", fcErr.Code, fcErr.Message))
}

// sendPortalNotice sends a notice from the bridge bot into a channel portal, if the room exists.
func (c *FChatClient) sendPortalNotice(channel, text string) {
	ctx := c.ctx()
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, makeChannelPortalKey(channel, c.UserLogin.ID))
	if err != nil || portal == nil || portal.MXID == "" {
		return
	}
	_, err = c.Main.Bridge.Bot.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{
		Parsed: &event.MessageEventContent{MsgType: event.MsgNotice, Body: text},
	}, nil)
	if err != nil {
		c.log.Err(err).Str("channel", channel).Msg("Failed to send notice to portal")
	}
}
