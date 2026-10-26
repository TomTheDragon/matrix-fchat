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

	// SpaceAvatar is the avatar the bridge last set on the login's space room.
	SpaceAvatar id.ContentURIString `json:"space_avatar,omitempty"`
}

type GhostMetadata struct {
	Gender string `json:"gender,omitempty"`
}

type PortalMetadata struct {
	// Title of the channel, used when the channel state isn't known yet (e.g. while disconnected).
	Title string `json:"title,omitempty"`
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
