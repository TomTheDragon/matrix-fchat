package connector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/fchat"
)

// F-Chat can only deliver private messages to characters that are online. Messages to
// offline characters are kept in the login metadata (so they survive restarts) and sent
// when the character comes online.

// maxQueuedPerCharacter is how many messages are kept for one offline character.
const maxQueuedPerCharacter = 50

// queuedMessage is a private message waiting for its recipient to come online.
type queuedMessage struct {
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
	Queued    int64  `json:"queued"`
	// The notice that told the user that the message is waiting. It's edited once the
	// message was sent.
	NoticeRoom  id.RoomID  `json:"notice_room,omitempty"`
	NoticeEvent id.EventID `json:"notice_event,omitempty"`
}

var errQueueFull = errors.New("too many messages are waiting for this character to come online")

// isOnline returns whether a character is known to be online.
func (c *FChatClient) isOnline(userID networkid.UserID) bool {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	info, ok := c.characters[userID]
	return ok && info.Status != "" && info.Status != fchat.StatusOffline
}

// queueOfflineMessage keeps a private message until the recipient comes online and tells
// the user about it with a notice (a reply to the Matrix message), which is edited later.
func (c *FChatClient) queueOfflineMessage(ctx context.Context, recipient, text string, original id.EventID) error {
	userID := makeUserID(recipient)
	c.metaLock.Lock()
	count := len(c.meta().OfflineMessages[string(userID)])
	c.metaLock.Unlock()
	if count >= maxQueuedPerCharacter {
		return errQueueFull
	}
	notice := fmt.Sprintf("⏳ %s is offline. The message will be sent when they come online.", recipient)
	roomID, eventID := c.sendDMNotice(ctx, userID, notice, original)
	c.metaLock.Lock()
	meta := c.meta()
	if meta.OfflineMessages == nil {
		meta.OfflineMessages = make(map[string][]queuedMessage)
	}
	meta.OfflineMessages[string(userID)] = append(meta.OfflineMessages[string(userID)], queuedMessage{
		Recipient:   recipient,
		Text:        text,
		Queued:      time.Now().Unix(),
		NoticeRoom:  roomID,
		NoticeEvent: eventID,
	})
	c.metaLock.Unlock()
	if err := c.UserLogin.Save(ctx); err != nil {
		c.log.Err(err).Msg("Failed to save queued message")
	}
	return nil
}

// hasOfflineMessages returns whether messages are waiting for a character.
func (c *FChatClient) hasOfflineMessages(userID networkid.UserID) bool {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	return len(c.meta().OfflineMessages[string(userID)]) > 0
}

// deliverOfflineMessages sends the waiting messages to all characters that are online now.
func (c *FChatClient) deliverOfflineMessages() {
	c.metaLock.Lock()
	var waiting []networkid.UserID
	for userID := range c.meta().OfflineMessages {
		waiting = append(waiting, networkid.UserID(userID))
	}
	c.metaLock.Unlock()
	for _, userID := range waiting {
		if c.isOnline(userID) {
			c.deliverOfflineMessagesTo(userID)
		}
	}
}

// deliverOfflineMessagesTo sends the waiting messages to a character that came online and
// edits their notices.
func (c *FChatClient) deliverOfflineMessagesTo(userID networkid.UserID) {
	c.offlineLock.Lock()
	defer c.offlineLock.Unlock()
	ctx := c.ctx()
	c.metaLock.Lock()
	messages := slices.Clone(c.meta().OfflineMessages[string(userID)])
	c.metaLock.Unlock()
	if len(messages) == 0 {
		return
	}
	sent := 0
	var sendErr error
	for _, msg := range messages {
		sendErr = c.Conn.SendMessage(ctx, fchat.CmdPrivate, &fchat.PrivateMessageRequest{Recipient: msg.Recipient, Message: msg.Text})
		if sendErr != nil {
			break
		}
		sent++
		queued := time.Unix(msg.Queued, 0).UTC().Format("2006-01-02 15:04 UTC")
		c.editNotice(ctx, userID, msg, fmt.Sprintf("✅ Sent to %s when they came online (waited since %s).", msg.Recipient, queued))
	}
	dropped := false
	var fcErr *fchat.Error
	if sendErr != nil {
		c.log.Warn().Err(sendErr).Str("character", messages[sent].Recipient).Msg("Failed to send queued message")
		if errors.As(sendErr, &fcErr) && fcErr.Code != fchat.ErrCodeCharacterNotFound {
			// The message won't ever be accepted (e.g. the character ignores you), so drop it.
			dropped = true
			c.editNotice(ctx, userID, messages[sent], fmt.Sprintf("❌ The message couldn't be sent to %s: %s", messages[sent].Recipient, fcErr.Message))
		}
	}
	done := sent
	if dropped {
		done++
	}
	c.metaLock.Lock()
	meta := c.meta()
	// Messages may have been added while sending.
	remaining := meta.OfflineMessages[string(userID)][done:]
	if len(remaining) == 0 {
		delete(meta.OfflineMessages, string(userID))
	} else {
		meta.OfflineMessages[string(userID)] = remaining
	}
	c.metaLock.Unlock()
	if err := c.UserLogin.Save(ctx); err != nil {
		c.log.Err(err).Msg("Failed to save login after sending queued messages")
	}
}

// editNotice replaces the text of a queued message's notice, or sends a new notice if the
// original one couldn't be sent.
func (c *FChatClient) editNotice(ctx context.Context, userID networkid.UserID, msg queuedMessage, text string) {
	if msg.NoticeRoom == "" || msg.NoticeEvent == "" {
		c.sendDMNotice(ctx, userID, text, "")
		return
	}
	content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: text, Mentions: &event.Mentions{}}
	content.SetEdit(msg.NoticeEvent)
	_, err := c.Main.Bridge.Bot.SendMessage(ctx, msg.NoticeRoom, event.EventMessage, &event.Content{Parsed: content}, nil)
	if err != nil {
		c.log.Err(err).Msg("Failed to edit queued message notice")
	}
}

// sendDMNotice sends a notice from the bridge bot into the DM room with a character, as a
// reply to replyTo if set. If the bot can't send there, the notice goes to the management
// room. It returns the room and event ID of the notice, or empty IDs if it couldn't be sent.
func (c *FChatClient) sendDMNotice(ctx context.Context, userID networkid.UserID, text string, replyTo id.EventID) (id.RoomID, id.EventID) {
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, makeDMPortalKey(string(userID), c.UserLogin.ID))
	if err == nil && portal != nil && portal.MXID != "" {
		content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: text, Mentions: &event.Mentions{}}
		if replyTo != "" {
			content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(replyTo)
		}
		resp, err := c.Main.Bridge.Bot.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{Parsed: content}, nil)
		if err == nil {
			return portal.MXID, resp.EventID
		}
		c.log.Debug().Err(err).Msg("Failed to send notice to DM room")
	}
	if !c.Main.Config.BridgeNotices {
		return "", ""
	}
	roomID, err := c.UserLogin.User.GetManagementRoom(ctx)
	if err != nil {
		c.log.Err(err).Msg("Failed to get management room to send notice")
		return "", ""
	}
	content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: text, Mentions: &event.Mentions{}}
	resp, err := c.Main.Bridge.Bot.SendMessage(ctx, roomID, event.EventMessage, &event.Content{Parsed: content}, nil)
	if err != nil {
		c.log.Err(err).Msg("Failed to send notice to management room")
		return "", ""
	}
	return roomID, resp.EventID
}
