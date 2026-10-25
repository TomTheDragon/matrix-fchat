package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"matrix-fchat/pkg/fchat"
)

type characterInfo struct {
	Name      string
	Gender    string
	Status    fchat.Status
	StatusMsg string
}

type channelState struct {
	ID          string
	Title       string
	Description string
	Mode        fchat.ChannelMode
	// Members maps user IDs to properly capitalized names.
	Members map[networkid.UserID]string
	Owner   networkid.UserID
	Ops     map[networkid.UserID]bool
	// Synced is set when the member list was small enough to be synced to Matrix.
	Synced bool
	// Initialized is set once ICH was received.
	Initialized bool
}

type FChatClient struct {
	Main      *FChatConnector
	UserLogin *bridgev2.UserLogin
	Conn      *fchat.Client

	log   zerolog.Logger
	ownID networkid.UserID

	stateLock  sync.RWMutex
	characters map[networkid.UserID]*characterInfo
	channels   map[string]*channelState
	friends    map[networkid.UserID]string
	ignored    map[networkid.UserID]bool

	metaLock       sync.Mutex
	badCredentials atomic.Bool
	// rejoining is set while the bridge rejoins channels after connecting, to
	// suppress "already in channel" errors.
	rejoining atomic.Bool

	firstConnectLock sync.Mutex
	firstConnect     chan error
}

var (
	_ bridgev2.NetworkAPI                    = (*FChatClient)(nil)
	_ bridgev2.IdentifierResolvingNetworkAPI = (*FChatClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*FChatClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*FChatClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*FChatClient)(nil)
	_ bridgev2.MembershipHandlingNetworkAPI  = (*FChatClient)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI   = (*FChatClient)(nil)
)

func (fc *FChatConnector) newClient(login *bridgev2.UserLogin) *FChatClient {
	meta := login.Metadata.(*UserLoginMetadata)
	log := login.Log.With().Str("component", "fchat").Logger()
	client := &FChatClient{
		Main:       fc,
		UserLogin:  login,
		log:        log,
		ownID:      makeUserID(meta.Character),
		characters: make(map[networkid.UserID]*characterInfo),
		channels:   make(map[string]*channelState),
		friends:    make(map[networkid.UserID]string),
		ignored:    make(map[networkid.UserID]bool),
	}
	client.Conn = fchat.NewClient(fchat.ClientConfig{
		ServerURL:     fc.Config.ServerURL,
		Account:       meta.Account,
		Password:      meta.Password,
		Character:     meta.Character,
		ClientName:    ClientName,
		ClientVersion: fc.versionOrDev(),
		UserAgent:     fc.userAgent(),
		Tickets:       fc.Tickets,
		ErrorWait:     fc.Config.ErrorWait(),
	}, log)
	client.Conn.Handler = client.handleCommand
	client.Conn.OnConnected = client.onConnected
	client.Conn.OnDisconnected = client.onDisconnected
	client.Conn.OnFatal = client.onFatal
	return client
}

func (fc *FChatConnector) versionOrDev() string {
	if fc.Version == "" {
		return "dev"
	}
	return fc.Version
}

func (c *FChatClient) meta() *UserLoginMetadata {
	return c.UserLogin.Metadata.(*UserLoginMetadata)
}

func (c *FChatClient) ctx() context.Context {
	return c.log.WithContext(context.Background())
}

// Connect starts the F-Chat connection loop. Errors are reported through bridge states.
func (c *FChatClient) Connect(ctx context.Context) {
	meta := c.meta()
	if meta.Account == "" || meta.Password == "" || meta.Character == "" {
		c.badCredentials.Store(true)
		c.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "fchat-missing-credentials",
			Message:    "Login data is missing, please log in again",
		})
		return
	}
	if c.Conn.IsRunning() {
		return
	}
	c.badCredentials.Store(false)
	c.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	c.Conn.Start()
}

func (c *FChatClient) Disconnect() {
	c.Conn.Stop()
}

func (c *FChatClient) IsLoggedIn() bool {
	return !c.badCredentials.Load()
}

func (c *FChatClient) LogoutRemote(ctx context.Context) {
	c.Conn.Stop()
	// API tickets can't be revoked, so just forget the credentials.
	c.metaLock.Lock()
	meta := c.meta()
	meta.Password = ""
	c.metaLock.Unlock()
}

func (c *FChatClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return userID == c.ownID
}

func (c *FChatClient) GetUserID() networkid.UserID {
	return c.ownID
}

// waitForFirstConnect is used during login to find out whether the credentials work.
func (c *FChatClient) waitForFirstConnect(ctx context.Context) error {
	c.firstConnectLock.Lock()
	ch := make(chan error, 1)
	c.firstConnect = ch
	c.firstConnectLock.Unlock()
	c.badCredentials.Store(false)
	c.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	c.Conn.Start()
	select {
	case err := <-ch:
		if err != nil {
			c.Conn.Stop()
		}
		return err
	case <-ctx.Done():
		c.Conn.Stop()
		return ctx.Err()
	case <-time.After(90 * time.Second):
		c.Conn.Stop()
		return errors.New("timed out connecting to F-Chat")
	}
}

func (c *FChatClient) signalFirstConnect(err error) {
	c.firstConnectLock.Lock()
	ch := c.firstConnect
	c.firstConnect = nil
	c.firstConnectLock.Unlock()
	if ch != nil {
		ch <- err
	}
}

func (c *FChatClient) onConnected() {
	c.signalFirstConnect(nil)
	c.badCredentials.Store(false)
	c.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	ctx := c.ctx()
	if msg := c.Main.Config.DefaultStatusMessage; msg != "" {
		err := c.Conn.Send(ctx, fchat.CmdStatus, &fchat.StatusRequest{Status: fchat.StatusOnline, StatusMsg: msg})
		if err != nil {
			c.log.Warn().Err(err).Msg("Failed to set default status")
		}
	}
	c.rejoinChannels(ctx)
}

func (c *FChatClient) rejoinChannels(ctx context.Context) {
	c.metaLock.Lock()
	channels := make([]string, 0, len(c.meta().Channels))
	for channel := range c.meta().Channels {
		channels = append(channels, channel)
	}
	c.metaLock.Unlock()
	if len(channels) == 0 {
		return
	}
	c.rejoining.Store(true)
	defer c.rejoining.Store(false)
	c.log.Debug().Strs("channels", channels).Msg("Rejoining channels")
	for _, channel := range channels {
		if err := c.Conn.Send(ctx, fchat.CmdJoinChannel, &fchat.ChannelRequest{Channel: channel}); err != nil {
			c.log.Warn().Err(err).Str("channel", channel).Msg("Failed to rejoin channel")
			return
		}
		// Don't flood the server with joins.
		time.Sleep(300 * time.Millisecond)
	}
	// Give the server some time to answer before re-enabling error notices.
	time.Sleep(2 * time.Second)
}

func (c *FChatClient) onDisconnected(err error) {
	c.signalFirstConnect(err)
	c.resetState()
	c.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateTransientDisconnect,
		Error:      "fchat-disconnected",
		Message:    err.Error(),
	})
}

func (c *FChatClient) onFatal(err error) {
	c.resetState()
	var fatal *fchat.FatalError
	var fcErr *fchat.Error
	state := status.BridgeState{
		StateEvent: status.StateUnknownError,
		Error:      "fchat-fatal",
		Message:    err.Error(),
	}
	if errors.As(err, &fcErr) {
		state.Error = status.BridgeStateErrorCode(fmt.Sprintf("fchat-%d", fcErr.Code))
		state.Message = fcErr.Message
	}
	if errors.As(err, &fatal) && fatal.BadCredentials {
		c.badCredentials.Store(true)
		state.StateEvent = status.StateBadCredentials
	}
	c.signalFirstConnect(err)
	c.UserLogin.BridgeState.Send(state)
	if !errors.As(err, &fatal) || !fatal.BadCredentials {
		c.sendManagementNotice(c.ctx(), fmt.Sprintf(
			"Disconnected from F-Chat: %s\n\nThe bridge won't reconnect automatically. Use `$cmdprefix reconnect` to connect again.",
			state.Message,
		))
	}
}

func (c *FChatClient) resetState() {
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	clear(c.characters)
	clear(c.channels)
}

// characterName returns the properly capitalized name of a character, if known.
func (c *FChatClient) characterName(userID networkid.UserID) string {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	if info, ok := c.characters[userID]; ok {
		return info.Name
	}
	if name, ok := c.friends[userID]; ok {
		return name
	}
	for _, ch := range c.channels {
		if name, ok := ch.Members[userID]; ok {
			return name
		}
	}
	if userID == c.ownID {
		return c.meta().Character
	}
	return ""
}

// rememberName stores a properly capitalized character name seen in an event.
func (c *FChatClient) rememberName(name string) {
	if name == "" {
		return
	}
	userID := makeUserID(name)
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	if _, ok := c.characters[userID]; !ok {
		c.characters[userID] = &characterInfo{Name: name, Status: fchat.StatusOffline}
	}
}

func (c *FChatClient) isIgnored(name string) bool {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	return c.ignored[makeUserID(name)]
}

func (c *FChatClient) sender(name string) bridgev2.EventSender {
	userID := makeUserID(name)
	if userID == c.ownID {
		return bridgev2.EventSender{IsFromMe: true, Sender: userID, SenderLogin: c.UserLogin.ID}
	}
	return bridgev2.EventSender{Sender: userID}
}

// sendManagementNotice sends a notice to the user's bridge management room.
func (c *FChatClient) sendManagementNotice(ctx context.Context, text string) {
	if !c.Main.Config.BridgeNotices {
		return
	}
	roomID, err := c.UserLogin.User.GetManagementRoom(ctx)
	if err != nil {
		c.log.Err(err).Msg("Failed to get management room to send notice")
		return
	}
	text = strings.ReplaceAll(text, "$cmdprefix", c.Main.Bridge.Config.CommandPrefix)
	content := format.RenderMarkdown(text, true, false)
	content.MsgType = event.MsgNotice
	_, err = c.Main.Bridge.Bot.SendMessage(ctx, roomID, event.EventMessage, &event.Content{Parsed: &content}, nil)
	if err != nil {
		c.log.Err(err).Msg("Failed to send notice to management room")
	}
}
