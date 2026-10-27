package connector

import (
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"
)

// UserLoginMetadata is stored in the user_login table.
//
// The password has to be stored because F-Chat API tickets expire after 30 minutes
// and every reconnect needs a fresh ticket.
type UserLoginMetadata struct {
	Account   string `json:"account"`
	Password  string `json:"password"`
	Character string `json:"character"`

	// Channels the character is in, mapped to their titles. F-Chat doesn't
	// remember joined channels between sessions, so the bridge rejoins these after connecting.
	Channels map[string]string `json:"channels,omitempty"`

	// FriendDMs are the friends (user IDs) that already got a DM room. Rooms are only
	// created once per friend, so a room the user left isn't created again.
	FriendDMs map[string]bool `json:"friend_dms,omitempty"`

	// ShowAds overrides the bridge_ads config option for this character (nil = use the config).
	ShowAds *bool `json:"show_ads,omitempty"`

	// OfflineMessages are private messages to offline characters (by user ID), which are
	// sent when the character comes online.
	OfflineMessages map[string][]queuedMessage `json:"offline_messages,omitempty"`

	// SpaceAvatar is the avatar the bridge last set on the login's space room.
	SpaceAvatar id.ContentURIString `json:"space_avatar,omitempty"`
}

type GhostMetadata struct {
	// Gender is the last known gender (F-Chat only tells it while the character is online).
	Gender string `json:"gender,omitempty"`
	// LastSeen is when the ghost's info was last updated (unix seconds), for the ghost cleanup.
	LastSeen int64 `json:"last_seen,omitempty"`
	// Removed is set when the ghost cleanup cleared the ghost's profile and rooms.
	Removed bool `json:"removed,omitempty"`
	// ProfileFieldsHash is the hash of the custom profile fields that were last set, so that
	// they're only sent to the homeserver when they changed.
	ProfileFieldsHash string `json:"profile_fields_hash,omitempty"`
	// ProfileSummary is the short summary of the F-List profile, for the Matrix biography.
	ProfileSummary string `json:"profile_summary,omitempty"`
	// ProfileFetched is when the F-List profile was last loaded (unix seconds).
	ProfileFetched int64 `json:"profile_fetched,omitempty"`
}

type PortalMetadata struct {
	// Title of the channel, used when the channel state isn't known yet (e.g. while disconnected).
	Title string `json:"title,omitempty"`
	// ShowAds overrides the character's ad setting for this channel (nil = use the character's setting).
	ShowAds *bool `json:"show_ads,omitempty"`
}

func (fc *FChatConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: func() any {
			return &PortalMetadata{}
		},
		Ghost: func() any {
			return &GhostMetadata{}
		},
		Message:  nil,
		Reaction: nil,
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}
