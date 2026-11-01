package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

var errMessageTooLong = bridgev2.WrapErrorInStatus(errors.New("message is too long for F-Chat")).
	WithIsCertain(true).WithErrorAsMessage().WithSendNotice(true).WithErrorReason(event.MessageStatusUnsupported)

var errNotConnected = bridgev2.WrapErrorInStatus(fchat.ErrNotConnected).
	WithIsCertain(true).WithErrorAsMessage().WithSendNotice(true)

// wrapSendError turns F-Chat errors into message status errors that are shown to the user.
func wrapSendError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fchat.ErrNotConnected) {
		return errNotConnected
	}
	var fcErr *fchat.Error
	if errors.As(err, &fcErr) {
		return bridgev2.WrapErrorInStatus(fcErr).
			WithIsCertain(true).WithErrorAsMessage().WithSendNotice(true).
			WithMessage(fcErr.Message)
	}
	return err
}

// htmlToBBCode converts Matrix HTML to F-Chat BBCode.
func (c *FChatClient) htmlToBBCode(ctx context.Context, html string) string {
	matrix := c.Main.Bridge.Matrix
	parser := &format.HTMLParser{
		TabsToSpaces:   4,
		Newline:        "\n",
		HorizontalLine: "\n---\n",
		BoldConverter: func(s string, _ format.Context) string {
			return "[b]" + s + "[/b]"
		},
		ItalicConverter: func(s string, _ format.Context) string {
			return "[i]" + s + "[/i]"
		},
		UnderlineConverter: func(s string, _ format.Context) string {
			return "[u]" + s + "[/u]"
		},
		StrikethroughConverter: func(s string, _ format.Context) string {
			return "[s]" + s + "[/s]"
		},
		SpoilerConverter: func(text, reason string, _ format.Context) string {
			return "[spoiler]" + text + "[/spoiler]"
		},
		MonospaceConverter: func(s string, _ format.Context) string {
			return "[noparse]" + s + "[/noparse]"
		},
		MonospaceBlockConverter: func(code, language string, _ format.Context) string {
			return "[noparse]" + strings.TrimRight(code, "\n") + "[/noparse]"
		},
		LinkConverter: func(text, href string, _ format.Context) string {
			if text == href || text == "" {
				return "[url]" + href + "[/url]"
			}
			return "[url=" + href + "]" + text + "[/url]"
		},
		ColorConverter: func(text, fg, bg string, _ format.Context) string {
			fg = strings.ToLower(fg)
			for name, hex := range bbcode.Colors {
				if hex == fg || name == fg {
					return "[color=" + name + "]" + text + "[/color]"
				}
			}
			return text
		},
		ImageConverter: func(src, alt, title, width, height string, isEmoji bool) string {
			if alt != "" {
				return alt
			}
			return title
		},
		PillConverter: func(displayname, mxid, eventID string, _ format.Context) string {
			if !strings.HasPrefix(mxid, "@") {
				return displayname
			}
			userID := id.UserID(mxid)
			if userID == c.UserLogin.UserMXID {
				return "[user]" + c.meta().Character + "[/user]"
			}
			if ghostID, ok := matrix.ParseGhostMXID(userID); ok {
				name := c.characterName(ghostID)
				if name == "" {
					name = displayname
				}
				return "[user]" + name + "[/user]"
			}
			return displayname
		},
	}
	return parser.Parse(html, format.NewContext(ctx))
}

func (c *FChatClient) messageText(ctx context.Context, content *event.MessageEventContent) string {
	if content.Format == event.FormatHTML && content.FormattedBody != "" {
		return c.htmlToBBCode(ctx, content.FormattedBody)
	}
	// Plain text is sent as-is, so F-Chat users can type BBCode directly.
	return content.Body
}

func (c *FChatClient) mediaText(ctx context.Context, content *event.MessageEventContent) (string, error) {
	publicMedia, ok := c.Main.Bridge.Matrix.(bridgev2.MatrixConnectorWithPublicMedia)
	if !ok {
		return "", bridgev2.ErrUnsupportedMessageType
	}
	url, err := publicMedia.GetPublicMediaAddressForEvent(ctx, content)
	if err != nil {
		return "", err
	} else if url == "" {
		return "", bridgev2.ErrPublicMediaDisabled
	}
	fileName := content.FileName
	caption := ""
	if fileName == "" {
		fileName = content.Body
	} else if content.Body != "" && content.Body != fileName {
		caption = c.messageText(ctx, content)
	}
	if fileName == "" {
		fileName = "file"
	}
	text := fmt.Sprintf("[url=%s]%s[/url]", url, fileName)
	if caption != "" {
		text = caption + "\n" + text
	}
	return text, nil
}

func (c *FChatClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	c.markActive(ctx)
	if !c.Conn.IsConnected() {
		return nil, errNotConnected
	}
	channel, dmUser, ok := parsePortalID(msg.Portal.ID)
	if !ok {
		return nil, fmt.Errorf("invalid portal ID %q", msg.Portal.ID)
	}
	content := msg.Content
	var text string
	switch content.MsgType {
	case event.MsgText, event.MsgNotice:
		text = c.messageText(ctx, content)
	case event.MsgEmote:
		text = "/me " + c.messageText(ctx, content)
	case event.MsgImage, event.MsgFile, event.MsgVideo, event.MsgAudio:
		var err error
		text, err = c.mediaText(ctx, content)
		if err != nil {
			return nil, err
		}
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	text = strings.TrimRight(text, "\n ")
	if text == "" {
		return nil, bridgev2.WrapErrorInStatus(errors.New("empty message")).WithIsCertain(true).WithErrorAsMessage()
	}

	var err error
	if channel != "" {
		if len(text) > c.Conn.ChatMax() {
			return nil, errMessageTooLong
		}
		err = c.Conn.SendMessage(ctx, fchat.CmdMessage, &fchat.ChannelMessage{Channel: channel, Message: text})
	} else {
		if len(text) > c.Conn.PrivMax() {
			return nil, errMessageTooLong
		}
		recipient := c.characterName(dmUser)
		if recipient == "" {
			if ghost, _ := c.Main.Bridge.GetExistingGhostByID(ctx, dmUser); ghost != nil && c.Main.ghostCharacterName(ghost) != "" {
				recipient = c.Main.ghostCharacterName(ghost)
			} else {
				recipient = string(dmUser)
			}
		}
		if c.Main.Config.QueueOfflineMessages && !c.isOnline(dmUser) {
			err = c.queueOfflineMessage(ctx, recipient, text, msg.Event.ID)
		} else {
			err = c.Conn.SendMessage(ctx, fchat.CmdPrivate, &fchat.PrivateMessageRequest{Recipient: recipient, Message: text})
			var fcErr *fchat.Error
			if c.Main.Config.QueueOfflineMessages && errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeCharacterNotFound {
				// The character went offline just now.
				err = c.queueOfflineMessage(ctx, recipient, text, msg.Event.ID)
			}
		}
	}
	if err != nil {
		return nil, wrapSendError(err)
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        makeMessageID(c.ownID),
			SenderID:  c.ownID,
			Timestamp: time.Now(),
		},
	}, nil
}

func (c *FChatClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if msg.IsTyping {
		c.markActive(ctx)
	}
	_, dmUser, ok := parsePortalID(msg.Portal.ID)
	if !ok || dmUser == "" || !c.Conn.IsConnected() {
		return nil
	}
	name := c.characterName(dmUser)
	if name == "" {
		return nil
	}
	typingStatus := fchat.TypingClear
	if msg.IsTyping {
		typingStatus = fchat.TypingActive
	}
	return c.Conn.Send(ctx, fchat.CmdTyping, &fchat.TypingRequest{Character: name, Status: typingStatus})
}

func (c *FChatClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	channel, _, ok := parsePortalID(msg.Portal.ID)
	if !ok || channel == "" {
		// Membership changes in DMs don't mean anything on F-Chat.
		return nil, nil
	}
	if msg.Type == bridgev2.Leave {
		return nil, c.Conn.Send(ctx, fchat.CmdLeaveChannel, &fchat.ChannelRequest{Channel: channel})
	}
	ghost, ok := msg.Target.(*bridgev2.Ghost)
	if !ok {
		return nil, nil
	}
	name := c.characterName(ghost.ID)
	if name == "" {
		name = c.Main.ghostCharacterName(ghost)
	}
	if name == "" {
		return nil, fmt.Errorf("unknown character %s", ghost.ID)
	}
	switch msg.Type {
	case bridgev2.Invite:
		return nil, c.Conn.Send(ctx, fchat.CmdChannelInvite, &fchat.ChannelCharacterRequest{Channel: channel, Character: name})
	case bridgev2.Kick:
		return nil, c.Conn.Send(ctx, fchat.CmdChannelKick, &fchat.ChannelCharacterRequest{Channel: channel, Character: name})
	case bridgev2.BanJoined, bridgev2.BanLeft, bridgev2.BanInvited:
		return nil, c.Conn.Send(ctx, fchat.CmdChannelBan, &fchat.ChannelCharacterRequest{Channel: channel, Character: name})
	case bridgev2.Unban:
		return nil, c.Conn.Send(ctx, fchat.CmdChannelUnban, &fchat.ChannelCharacterRequest{Channel: channel, Character: name})
	}
	return nil, nil
}

func (c *FChatClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	channel, _, ok := parsePortalID(msg.Portal.ID)
	if !ok || channel == "" {
		return false, bridgev2.ErrRoomMetadataNotAllowed
	}
	err := c.Conn.Send(ctx, fchat.CmdChannelDesc, &fchat.DescriptionChange{Channel: channel, Description: msg.Content.Topic})
	if err != nil {
		return false, err
	}
	c.stateLock.Lock()
	if ch, ok := c.channels[channel]; ok {
		ch.Description = msg.Content.Topic
	}
	c.stateLock.Unlock()
	return true, nil
}
