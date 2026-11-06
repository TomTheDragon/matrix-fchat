package connector

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// Character names are case-insensitive on F-List, so all IDs use the lowercase form.
// The properly capitalized name is used as the display name.

func makeUserID(character string) networkid.UserID {
	return networkid.UserID(strings.ToLower(strings.TrimSpace(character)))
}

func makeUserLoginID(character string) networkid.UserLoginID {
	return networkid.UserLoginID(makeUserID(character))
}

// broadcastUserID is the ghost that sends F-Chat broadcasts (BRO). Character names can't
// contain dots, so it never collides with a character.
const (
	broadcastUserID   = networkid.UserID("fchat.broadcast")
	broadcastUserName = "F-Chat Broadcast"
)

const (
	channelPrefix = "c:"
	dmPrefix      = "d:"
)

func makeChannelPortalKey(channel string, login networkid.UserLoginID) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(channelPrefix + channel), Receiver: login}
}

func makeDMPortalKey(character string, login networkid.UserLoginID) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(dmPrefix + string(makeUserID(character))), Receiver: login}
}

// parsePortalID returns the channel ID or the DM partner's user ID.
func parsePortalID(id networkid.PortalID) (channel string, dmUser networkid.UserID, ok bool) {
	s := string(id)
	switch {
	case strings.HasPrefix(s, channelPrefix):
		return s[len(channelPrefix):], "", true
	case strings.HasPrefix(s, dmPrefix):
		return "", networkid.UserID(s[len(dmPrefix):]), true
	}
	return "", "", false
}

// makeMessageID generates a unique message ID. F-Chat messages don't have IDs,
// so they're only used to keep track of messages in the bridge database.
func makeMessageID(sender networkid.UserID) networkid.MessageID {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return networkid.MessageID(fmt.Sprintf("%d.%s.%s", time.Now().UnixMilli(), hex.EncodeToString(buf[:]), sender))
}

// isValidCharacterName checks the rough shape of an F-List character name:
// letters, digits, spaces, dashes and underscores, up to about 20 characters (checked leniently).
func isValidCharacterName(name string) bool {
	if name == "" || len(name) > 32 || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
