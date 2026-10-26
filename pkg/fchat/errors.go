package fchat

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Error is an ERR command sent by the server.
type Error struct {
	Code    int    `json:"number"`
	Message string `json:"message"`
}

// UnmarshalJSON accepts both the documented "number" field and the "code" field
// used in some newer documentation examples.
func (e *Error) UnmarshalJSON(data []byte) error {
	var raw struct {
		Number  *int   `json:"number"`
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch {
	case raw.Number != nil:
		e.Code = *raw.Number
	case raw.Code != nil:
		e.Code = *raw.Code
	}
	e.Message = raw.Message
	return nil
}

func (e *Error) Error() string {
	return fmt.Sprintf("F-Chat error %d: %s", e.Code, e.Message)
}

// Is makes errors.Is match on the error code.
func (e *Error) Is(target error) bool {
	var other *Error
	if errors.As(target, &other) {
		return other.Code == e.Code
	}
	return false
}

// Error codes from https://wiki.f-list.net/F-Chat_Error_Codes
const (
	ErrCodeSyntax             = 1
	ErrCodeServerFull         = 2
	ErrCodeNotLoggedIn        = 3
	ErrCodeIdentifyFailed     = 4
	ErrCodeMessageFlood       = 5
	ErrCodeCharacterNotFound  = 6
	ErrCodeUnknownCommand     = 8
	ErrCodeBanned             = 9
	ErrCodeAlreadyIdentified  = 11
	ErrCodeMessageTooLong     = 15
	ErrCodeNoSearchResults    = 18
	ErrCodeIgnored            = 20
	ErrCodeChannelNotFound    = 26
	ErrCodeAlreadyInChannel   = 28
	ErrCodeTooManyConnections = 30
	ErrCodeLoggedInElsewhere  = 31
	ErrCodeUnknownAuthMethod  = 33
	ErrCodeTimedOut           = 39
	ErrCodeKicked             = 40
	ErrCodeInviteOnly         = 44
	ErrCodeNotInChannel       = 45
	ErrCodeBannedFromChannel  = 48
	ErrCodeAdFlood            = 56
	ErrCodeSearchThrottled    = 50
	ErrCodeNoAdsAllowed       = 59
	ErrCodeNoChatAllowed      = 60
	ErrCodeNoLoginSlots       = 62
	ErrCodeTooManyResults     = 72
	ErrCodeLoginTimedOut      = -4
)

// IsNoReconnect returns true for the error codes after which the server drops
// the connection and clients must not reconnect automatically.
// See "Server closes connection after issuing an ERR protocol command" in the API docs.
func IsNoReconnect(code int) bool {
	switch code {
	case ErrCodeServerFull, ErrCodeBanned, ErrCodeTooManyConnections, ErrCodeLoggedInElsewhere,
		ErrCodeUnknownAuthMethod, ErrCodeTimedOut, ErrCodeKicked:
		return true
	}
	return false
}

// FatalError wraps an error after which the client stops reconnecting.
type FatalError struct {
	Err error
	// BadCredentials is set when the failure is caused by the account login data.
	BadCredentials bool
}

func (fe *FatalError) Error() string {
	return fe.Err.Error()
}

func (fe *FatalError) Unwrap() error {
	return fe.Err
}
