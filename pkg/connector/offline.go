package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

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
}

var errQueueFull = errors.New("too many messages are waiting for this character to come online")

// isOnline returns whether a character is known to be online.
func (c *FChatClient) isOnline(userID networkid.UserID) bool {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	info, ok := c.characters[userID]
	return ok && info.Status != "" && info.Status != fchat.StatusOffline
}

// queueOfflineMessage keeps a private message until the recipient comes online.
func (c *FChatClient) queueOfflineMessage(ctx context.Context, recipient, text string) error {
	userID := makeUserID(recipient)
	c.metaLock.Lock()
	meta := c.meta()
	if len(meta.OfflineMessages[string(userID)]) >= maxQueuedPerCharacter {
		c.metaLock.Unlock()
		return errQueueFull
	}
	if meta.OfflineMessages == nil {
		meta.OfflineMessages = make(map[string][]queuedMessage)
	}
	meta.OfflineMessages[string(userID)] = append(meta.OfflineMessages[string(userID)], queuedMessage{
		Recipient: recipient,
		Text:      text,
		Queued:    time.Now().Unix(),
	})
	count := len(meta.OfflineMessages[string(userID)])
	c.metaLock.Unlock()
	if err := c.UserLogin.Save(ctx); err != nil {
		c.log.Err(err).Msg("Failed to save queued message")
	}
	notice := fmt.Sprintf("%s is offline. The message will be sent when they come online", recipient)
	if count > 1 {
		notice += fmt.Sprintf(" (%d messages waiting)", count)
	}
	c.sendDMNotice(ctx, userID, notice+".")
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

// deliverOfflineMessagesTo sends the waiting messages to a character that came online.
func (c *FChatClient) deliverOfflineMessagesTo(userID networkid.UserID) {
	c.offlineLock.Lock()
	defer c.offlineLock.Unlock()
	ctx := c.ctx()
	c.metaLock.Lock()
	messages := c.meta().OfflineMessages[string(userID)]
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
	}
	c.metaLock.Lock()
	meta := c.meta()
	// Messages may have been added while sending.
	remaining := meta.OfflineMessages[string(userID)][sent:]
	if len(remaining) == 0 {
		delete(meta.OfflineMessages, string(userID))
	} else {
		meta.OfflineMessages[string(userID)] = remaining
	}
	c.metaLock.Unlock()
	if err := c.UserLogin.Save(ctx); err != nil {
		c.log.Err(err).Msg("Failed to save login after sending queued messages")
	}
	name := messages[0].Recipient
	if sent > 0 {
		c.sendDMNotice(ctx, userID, fmt.Sprintf("%s is online now, sent %d waiting message(s).", name, sent))
	}
	if sendErr != nil {
		c.log.Warn().Err(sendErr).Str("character", name).Msg("Failed to send queued message")
		var fcErr *fchat.Error
		if errors.As(sendErr, &fcErr) && fcErr.Code != fchat.ErrCodeCharacterNotFound {
			// The message won't ever be accepted (e.g. the character ignores you), so drop it.
			c.dropFirstOfflineMessage(ctx, userID)
			c.sendDMNotice(ctx, userID, fmt.Sprintf("A waiting message couldn't be sent to %s: %s", name, fcErr.Message))
		}
	}
}

func (c *FChatClient) dropFirstOfflineMessage(ctx context.Context, userID networkid.UserID) {
	c.metaLock.Lock()
	meta := c.meta()
	if queue := meta.OfflineMessages[string(userID)]; len(queue) > 1 {
		meta.OfflineMessages[string(userID)] = queue[1:]
	} else {
		delete(meta.OfflineMessages, string(userID))
	}
	c.metaLock.Unlock()
	if err := c.UserLogin.Save(ctx); err != nil {
		c.log.Err(err).Msg("Failed to save login")
	}
}

// sendDMNotice sends a notice from the bridge bot into the DM room with a character.
// If the bot can't send there, the notice goes to the management room.
func (c *FChatClient) sendDMNotice(ctx context.Context, userID networkid.UserID, text string) {
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, makeDMPortalKey(string(userID), c.UserLogin.ID))
	if err == nil && portal != nil && portal.MXID != "" {
		_, err = c.Main.Bridge.Bot.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{
			Parsed: &event.MessageEventContent{MsgType: event.MsgNotice, Body: text, Mentions: &event.Mentions{}},
		}, nil)
		if err == nil {
			return
		}
		c.log.Debug().Err(err).Msg("Failed to send notice to DM room")
	}
	c.sendManagementNotice(ctx, text)
}
