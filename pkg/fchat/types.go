// Package fchat implements a client for the F-Chat websocket protocol and the
// parts of the F-List JSON API that a chat client needs.
//
// Protocol reference: https://wiki.f-list.net/F-Chat_Protocol
package fchat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Command codes sent by the server and/or the client.
const (
	CmdAdminList      = "ADL"
	CmdBroadcast      = "BRO"
	CmdChannelDesc    = "CDS"
	CmdPublicChannels = "CHA"
	CmdChannelInvite  = "CIU"
	CmdChannelBan     = "CBU"
	CmdChannelBanList = "CBL"
	CmdChannelUnban   = "CUB"
	CmdChannelKick    = "CKU"
	CmdOpAdd          = "COA"
	CmdOpList         = "COL"
	CmdOpRemove       = "COR"
	CmdConnectedCount = "CON"
	CmdCreateChannel  = "CCR"
	CmdSetOwner       = "CSO"
	CmdChannelTimeout = "CTU"
	CmdError          = "ERR"
	CmdFriendList     = "FRL"
	CmdOffline        = "FLN"
	CmdHello          = "HLO"
	CmdInitialChannel = "ICH"
	CmdIdentify       = "IDN"
	CmdIgnore         = "IGN"
	CmdJoinChannel    = "JCH"
	CmdLeaveChannel   = "LCH"
	CmdOnlineList     = "LIS"
	CmdOnline         = "NLN"
	CmdOpenRooms      = "ORS"
	CmdPing           = "PIN"
	CmdPrivate        = "PRI"
	CmdMessage        = "MSG"
	CmdAd             = "LRP"
	CmdRoll           = "RLL"
	CmdRoomMode       = "RMO"
	CmdRoomStatus     = "RST"
	CmdRealtimeBridge = "RTB"
	CmdSearch         = "FKS"
	CmdStatus         = "STA"
	CmdSystem         = "SYS"
	CmdTyping         = "TPN"
	CmdUptime         = "UPT"
	CmdVariable       = "VAR"
)

// Status is a character's online status.
type Status string

const (
	StatusOnline  Status = "online"
	StatusLooking Status = "looking"
	StatusBusy    Status = "busy"
	StatusDND     Status = "dnd"
	StatusIdle    Status = "idle"
	StatusAway    Status = "away"
	StatusCrown   Status = "crown"
	StatusOffline Status = "offline"
)

// SettableStatuses are the statuses a client is allowed to set with STA.
var SettableStatuses = []Status{StatusOnline, StatusLooking, StatusBusy, StatusDND, StatusIdle, StatusAway}

// TypingStatus is the value of a TPN command.
type TypingStatus string

const (
	TypingClear  TypingStatus = "clear"
	TypingPaused TypingStatus = "paused"
	TypingActive TypingStatus = "typing"
)

// ChannelMode describes which kinds of messages a channel accepts.
type ChannelMode string

const (
	ModeChat ChannelMode = "chat"
	ModeAds  ChannelMode = "ads"
	ModeBoth ChannelMode = "both"
)

// RawCommand is a single protocol frame: a three letter code and an optional JSON payload.
type RawCommand struct {
	Code    string
	Payload json.RawMessage
}

// ParseCommand splits a raw frame into its code and payload.
func ParseCommand(data []byte) (RawCommand, error) {
	if len(data) < 3 {
		return RawCommand{}, fmt.Errorf("command too short: %q", data)
	}
	cmd := RawCommand{Code: string(data[:3])}
	rest := strings.TrimSpace(string(data[3:]))
	if rest != "" {
		cmd.Payload = json.RawMessage(rest)
	}
	return cmd, nil
}

// Serialize builds a frame for sending. A nil payload produces an argumentless command.
func Serialize(code string, payload any) ([]byte, error) {
	if len(code) != 3 {
		return nil, fmt.Errorf("invalid command code %q", code)
	}
	if payload == nil {
		return []byte(code), nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 4+len(data))
	out = append(out, code...)
	out = append(out, ' ')
	out = append(out, data...)
	return out, nil
}

// Character is the {"identity": "..."} object used in JCH and ICH.
type Character struct {
	Identity string `json:"identity"`
}

// IdentifyRequest is the client IDN payload.
type IdentifyRequest struct {
	Method    string `json:"method"`
	Account   string `json:"account"`
	Ticket    string `json:"ticket"`
	Character string `json:"character"`
	CName     string `json:"cname"`
	CVersion  string `json:"cversion"`
}

type IdentifyResponse struct {
	Character string `json:"character"`
}

type ChannelRequest struct {
	Channel string `json:"channel"`
}

type ChannelCharacterRequest struct {
	Channel   string `json:"channel"`
	Character string `json:"character"`
}

type ChannelMessage struct {
	Channel   string `json:"channel"`
	Character string `json:"character,omitempty"`
	Message   string `json:"message"`
}

type PrivateMessageRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type PrivateMessage struct {
	Character string `json:"character"`
	Message   string `json:"message"`
}

type RollRequest struct {
	Channel string `json:"channel"`
	Dice    string `json:"dice"`
}

type Roll struct {
	Channel   string   `json:"channel"`
	Recipient string   `json:"recipient,omitempty"`
	Type      string   `json:"type"`
	Message   string   `json:"message"`
	Character string   `json:"character"`
	Target    string   `json:"target,omitempty"`
	Rolls     []string `json:"rolls,omitempty"`
	Results   []int    `json:"results,omitempty"`
	EndResult int      `json:"endresult,omitempty"`
}

type TypingRequest struct {
	Character string       `json:"character"`
	Status    TypingStatus `json:"status"`
}

type StatusRequest struct {
	Status    Status `json:"status"`
	StatusMsg string `json:"statusmsg"`
}

type StatusChange struct {
	Character string `json:"character"`
	Status    Status `json:"status"`
	StatusMsg string `json:"statusmsg"`
}

type CreateChannelRequest struct {
	Channel string `json:"channel"`
}

type DescriptionChange struct {
	Channel     string `json:"channel"`
	Description string `json:"description"`
}

type JoinChannel struct {
	Channel   string    `json:"channel"`
	Character Character `json:"character"`
	Title     string    `json:"title"`
}

type LeaveChannel struct {
	Channel   string `json:"channel"`
	Character string `json:"character"`
}

type InitialChannelData struct {
	Channel string      `json:"channel"`
	Mode    ChannelMode `json:"mode"`
	Users   []Character `json:"users"`
}

type OpList struct {
	Channel string   `json:"channel"`
	OpList  []string `json:"oplist"`
}

type OpChange struct {
	Channel   string `json:"channel"`
	Character string `json:"character"`
}

type ChannelModeration struct {
	Operator  string `json:"operator"`
	Channel   string `json:"channel"`
	Character string `json:"character"`
	Length    int    `json:"length,omitempty"`
}

type ChannelInvite struct {
	Sender string `json:"sender"`
	Title  string `json:"title"`
	Name   string `json:"name"`
}

type RoomModeChange struct {
	Channel string      `json:"channel"`
	Mode    ChannelMode `json:"mode"`
}

type OnlineNotice struct {
	Identity string `json:"identity"`
	Gender   string `json:"gender"`
	Status   Status `json:"status"`
}

type OfflineNotice struct {
	Character string `json:"character"`
}

// OnlineList is a LIS batch. Each entry is [name, gender, status, statusmsg].
type OnlineList struct {
	Characters [][]string `json:"characters"`
}

type FriendList struct {
	Characters []string `json:"characters"`
}

type IgnoreList struct {
	Action     string   `json:"action"`
	Characters []string `json:"characters,omitempty"`
	Character  string   `json:"character,omitempty"`
}

type SystemMessage struct {
	Message string `json:"message"`
	Channel string `json:"channel,omitempty"`
}

type Broadcast struct {
	Message   string `json:"message"`
	Character string `json:"character,omitempty"`
}

type RealtimeBridge struct {
	Type      string `json:"type"`
	Character string `json:"character,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Title     string `json:"title,omitempty"`
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
}

type Variable struct {
	Variable string          `json:"variable"`
	Value    json.RawMessage `json:"value"`
}

type PublicChannel struct {
	Name       string      `json:"name"`
	Mode       ChannelMode `json:"mode"`
	Characters int         `json:"characters"`
}

type PublicChannelList struct {
	Channels []PublicChannel `json:"channels"`
}

type OpenRoom struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Characters int    `json:"characters"`
}

type OpenRoomList struct {
	Channels []OpenRoom `json:"channels"`
}

type IgnoreRequest struct {
	Action    string `json:"action"`
	Character string `json:"character,omitempty"`
}

type ChannelTimeoutRequest struct {
	Channel   string `json:"channel"`
	Character string `json:"character"`
	Length    int    `json:"length"`
}

// IsPrivateChannelID returns true if the channel identifier refers to a
// user-created room (ADH-xxxx) rather than an official public channel.
func IsPrivateChannelID(channel string) bool {
	return strings.HasPrefix(strings.ToUpper(channel), "ADH-")
}

// SearchRequest is the payload of FKS (character search). Kinks are kink IDs.
type SearchRequest struct {
	Kinks        []string `json:"kinks"`
	Genders      []string `json:"genders,omitempty"`
	Orientations []string `json:"orientations,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	FurryPrefs   []string `json:"furryprefs,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	Positions    []string `json:"positions,omitempty"`
}

// SearchResult is the server's FKS response: the names of the matching characters that are online.
type SearchResult struct {
	Characters []string `json:"characters"`
}

// ChannelBanListRequest asks for a channel's ban list (CBL). The server answers with a SYS message.
type ChannelBanListRequest = ChannelRequest

// RoomStatusRequest changes whether a private room is open to everyone ("public") or
// invite-only ("private").
type RoomStatusRequest struct {
	Channel string `json:"channel"`
	Status  string `json:"status"`
}

// Uptime is the server's UPT response.
type Uptime struct {
	StartString string `json:"startstring"`
	StartTime   int64  `json:"starttime"`
	Channels    int    `json:"channels"`
	MaxUsers    int    `json:"maxusers"`
	Users       int    `json:"users"`
	Accepted    int64  `json:"accepted"`
	Time        int64  `json:"time"`
}
