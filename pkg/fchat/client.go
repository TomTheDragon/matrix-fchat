package fchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

// ClientConfig contains everything needed to log in a single character.
type ClientConfig struct {
	ServerURL string
	Account   string
	Password  string
	Character string

	ClientName    string
	ClientVersion string
	UserAgent     string

	Tickets *TicketCache

	// ErrorWait is how long SendMessage waits for an ERR response after sending
	// a chat message. The protocol doesn't acknowledge successful messages, so
	// errors can only be correlated by timing.
	ErrorWait time.Duration
}

// Handler receives all server commands that aren't consumed by the client itself.
// It is called from a single goroutine, in the order the commands were received.
type Handler func(cmd RawCommand)

// Client is a persistent, automatically reconnecting F-Chat connection for one character.
type Client struct {
	cfg ClientConfig
	log zerolog.Logger

	// Callbacks. They must be set before Start is called.
	Handler        Handler
	OnConnected    func()
	OnDisconnected func(err error)
	OnFatal        func(err error)

	runLock sync.Mutex
	stop    context.CancelFunc
	done    chan struct{}

	connLock   sync.Mutex
	conn       *websocket.Conn
	writeLock  sync.Mutex
	identified atomic.Bool

	msgLock sync.Mutex
	lastMsg time.Time

	pendingErrLock sync.Mutex
	pendingErr     chan *Error

	waitersLock sync.Mutex
	waiters     map[string][]chan json.RawMessage

	varsLock sync.RWMutex
	vars     map[string]json.RawMessage

	dispatch chan RawCommand
}

func NewClient(cfg ClientConfig, log zerolog.Logger) *Client {
	if cfg.ServerURL == "" {
		cfg.ServerURL = DefaultServerURL
	}
	if cfg.ErrorWait <= 0 {
		cfg.ErrorWait = time.Second
	}
	return &Client{
		cfg:     cfg,
		log:     log,
		waiters: make(map[string][]chan json.RawMessage),
		vars:    make(map[string]json.RawMessage),
	}
}

func (c *Client) Character() string {
	return c.cfg.Character
}

// IsConnected returns true if the connection is open and the character has been identified.
func (c *Client) IsConnected() bool {
	return c.identified.Load()
}

// IsRunning returns true if the connection loop is active.
func (c *Client) IsRunning() bool {
	c.runLock.Lock()
	defer c.runLock.Unlock()
	return c.stop != nil
}

// Start starts the connection loop in the background. It does nothing if already running.
func (c *Client) Start() {
	c.runLock.Lock()
	defer c.runLock.Unlock()
	if c.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.stop = cancel
	c.done = make(chan struct{})
	c.dispatch = make(chan RawCommand, 4096)
	go c.dispatchLoop(c.dispatch)
	go c.runLoop(ctx, c.done, c.dispatch)
}

// Stop disconnects and stops reconnecting. It blocks until the connection loop has exited.
func (c *Client) Stop() {
	c.runLock.Lock()
	stop, done := c.stop, c.done
	c.stop = nil
	c.runLock.Unlock()
	if stop == nil {
		return
	}
	stop()
	c.closeConn()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.log.Warn().Msg("Timed out waiting for connection loop to exit")
	}
}

func (c *Client) closeConn() {
	c.connLock.Lock()
	conn := c.conn
	c.connLock.Unlock()
	if conn != nil {
		// The server doesn't implement the close handshake, so don't wait for it.
		_ = conn.CloseNow()
	}
}

func (c *Client) dispatchLoop(ch <-chan RawCommand) {
	for cmd := range ch {
		c.safeHandle(cmd)
	}
}

func (c *Client) safeHandle(cmd RawCommand) {
	defer func() {
		if err := recover(); err != nil {
			c.log.Error().Any("panic", err).Str("command", cmd.Code).Msg("Panic in command handler")
		}
	}()
	if c.Handler != nil {
		c.Handler(cmd)
	}
}

func (c *Client) runLoop(ctx context.Context, done chan struct{}, dispatch chan RawCommand) {
	defer close(done)
	defer close(dispatch)
	backoff := 2 * time.Second
	forceNewTicket := false
	identifyFailures := 0
	for {
		start := time.Now()
		err := c.session(ctx, forceNewTicket, dispatch)
		c.identified.Store(false)
		if ctx.Err() != nil {
			return
		}
		forceNewTicket = false
		var fatal *FatalError
		var fcErr *Error
		switch {
		case errors.As(err, &fatal):
			c.log.Err(err).Msg("Fatal F-Chat error, not reconnecting")
			c.markStopped()
			if c.OnFatal != nil {
				c.OnFatal(err)
			}
			return
		case errors.As(err, &fcErr) && fcErr.Code == ErrCodeIdentifyFailed:
			// The ticket was probably invalidated by another client. Retry once with a new one.
			identifyFailures++
			if identifyFailures > 1 {
				fatalErr := &FatalError{Err: err, BadCredentials: true}
				c.log.Err(err).Msg("Identification failed repeatedly, not reconnecting")
				c.markStopped()
				if c.OnFatal != nil {
					c.OnFatal(fatalErr)
				}
				return
			}
			forceNewTicket = true
		default:
			identifyFailures = 0
		}
		if time.Since(start) > 2*time.Minute {
			backoff = 2 * time.Second
		}
		c.log.Warn().Err(err).Stringer("retry_in", backoff).Msg("Disconnected from F-Chat")
		if c.OnDisconnected != nil {
			c.OnDisconnected(err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 5*time.Minute)
	}
}

func (c *Client) markStopped() {
	c.runLock.Lock()
	c.stop = nil
	c.runLock.Unlock()
}

var errReadTimeout = errors.New("no data received from server")

func (c *Client) session(ctx context.Context, forceNewTicket bool, dispatch chan<- RawCommand) error {
	ticket, err := c.cfg.Tickets.Get(ctx, c.cfg.Account, c.cfg.Password, forceNewTicket)
	if errors.Is(err, ErrInvalidLogin) {
		return &FatalError{Err: err, BadCredentials: true}
	} else if err != nil {
		return fmt.Errorf("failed to get API ticket: %w", err)
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	header := http.Header{}
	if c.cfg.UserAgent != "" {
		header.Set("User-Agent", c.cfg.UserAgent)
	}
	conn, _, err := websocket.Dial(dialCtx, c.cfg.ServerURL, &websocket.DialOptions{HTTPHeader: header})
	cancelDial()
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", c.cfg.ServerURL, err)
	}
	// Online lists (LIS) can be very large.
	conn.SetReadLimit(64 * 1024 * 1024)
	c.connLock.Lock()
	c.conn = conn
	c.connLock.Unlock()
	defer func() {
		c.connLock.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.connLock.Unlock()
		_ = conn.CloseNow()
	}()

	c.log.Debug().Str("character", c.cfg.Character).Msg("Connected to F-Chat, identifying")
	err = c.writeRaw(ctx, conn, CmdIdentify, &IdentifyRequest{
		Method:    "ticket",
		Account:   c.cfg.Account,
		Ticket:    ticket.Ticket,
		Character: c.cfg.Character,
		CName:     c.cfg.ClientName,
		CVersion:  c.cfg.ClientVersion,
	})
	if err != nil {
		return fmt.Errorf("failed to send identification: %w", err)
	}

	identified := false
	for {
		// Before identification the server should answer quickly. Afterwards it
		// pings every 30 seconds and disconnects after three missed pings.
		timeout := 3 * time.Minute
		if !identified {
			timeout = 60 * time.Second
		}
		readCtx, cancelRead := context.WithTimeout(ctx, timeout)
		_, data, err := conn.Read(readCtx)
		timedOut := readCtx.Err() != nil && ctx.Err() == nil
		cancelRead()
		if err != nil {
			if timedOut {
				return errReadTimeout
			}
			return fmt.Errorf("failed to read from websocket: %w", err)
		}
		cmd, err := ParseCommand(data)
		if err != nil {
			c.log.Warn().Err(err).Msg("Ignoring malformed command")
			continue
		}
		if cmd.Code != CmdPing && c.log.Trace().Enabled() {
			c.log.Trace().Str("code", cmd.Code).RawJSON("payload", payloadForLog(cmd.Payload)).Msg("Received command")
		}
		switch cmd.Code {
		case CmdPing:
			if err = c.writeRaw(ctx, conn, CmdPing, nil); err != nil {
				return fmt.Errorf("failed to respond to ping: %w", err)
			}
			continue
		case CmdIdentify:
			identified = true
			c.identified.Store(true)
			c.log.Info().Str("character", c.cfg.Character).Msg("Identified with F-Chat server")
			if c.OnConnected != nil {
				go c.OnConnected()
			}
			continue
		case CmdError:
			var fcErr Error
			if err = json.Unmarshal(cmd.Payload, &fcErr); err != nil {
				c.log.Warn().Err(err).Msg("Failed to parse ERR command")
				continue
			}
			if fcErr.Code == ErrCodeIdentifyFailed {
				c.cfg.Tickets.Invalidate(c.cfg.Account, ticket.Ticket)
				return &fcErr
			} else if IsNoReconnect(fcErr.Code) {
				return &FatalError{Err: &fcErr}
			} else if !identified && (fcErr.Code == ErrCodeNoLoginSlots || fcErr.Code == ErrCodeLoginTimedOut) {
				return &fcErr
			}
			if c.deliverError(&fcErr) {
				continue
			}
		case CmdVariable:
			var v Variable
			if json.Unmarshal(cmd.Payload, &v) == nil {
				c.varsLock.Lock()
				c.vars[v.Variable] = v.Value
				c.varsLock.Unlock()
			}
		}
		if c.deliverToWaiter(cmd) {
			continue
		}
		select {
		case dispatch <- cmd:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func payloadForLog(p json.RawMessage) json.RawMessage {
	if len(p) == 0 {
		return json.RawMessage("null")
	} else if len(p) > 2000 {
		return json.RawMessage(strconv.Quote(string(p[:2000]) + "…"))
	}
	return p
}

func (c *Client) writeRaw(ctx context.Context, conn *websocket.Conn, code string, payload any) error {
	data, err := Serialize(code, payload)
	if err != nil {
		return err
	}
	c.writeLock.Lock()
	defer c.writeLock.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if code != CmdPing && c.log.Trace().Enabled() {
		c.log.Trace().Str("code", code).Msg("Sending command")
	}
	return conn.Write(writeCtx, websocket.MessageText, data)
}

// ErrNotConnected is returned when trying to send while the connection is down.
var ErrNotConnected = errors.New("not connected to F-Chat")

// Send sends a command without waiting for any response.
func (c *Client) Send(ctx context.Context, code string, payload any) error {
	c.connLock.Lock()
	conn := c.conn
	c.connLock.Unlock()
	if conn == nil || !c.identified.Load() {
		return ErrNotConnected
	}
	return c.writeRaw(ctx, conn, code, payload)
}

// SendMessage sends a flood-controlled command (MSG, PRI, LRP, RLL) and waits
// briefly for an ERR response. Messages are serialized and spaced according to
// the msg_flood server variable.
func (c *Client) SendMessage(ctx context.Context, code string, payload any) error {
	c.msgLock.Lock()
	defer c.msgLock.Unlock()

	flood := c.MessageFloodInterval()
	if wait := time.Until(c.lastMsg.Add(flood)); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	errCh := make(chan *Error, 1)
	c.pendingErrLock.Lock()
	c.pendingErr = errCh
	c.pendingErrLock.Unlock()
	defer func() {
		c.pendingErrLock.Lock()
		if c.pendingErr == errCh {
			c.pendingErr = nil
		}
		c.pendingErrLock.Unlock()
	}()

	err := c.Send(ctx, code, payload)
	c.lastMsg = time.Now()
	if err != nil {
		return err
	}
	select {
	case fcErr := <-errCh:
		return fcErr
	case <-time.After(c.cfg.ErrorWait):
		return nil
	case <-ctx.Done():
		// The message was already sent, so don't report a failure.
		return nil
	}
}

func (c *Client) deliverError(fcErr *Error) bool {
	c.pendingErrLock.Lock()
	defer c.pendingErrLock.Unlock()
	if c.pendingErr == nil {
		return false
	}
	select {
	case c.pendingErr <- fcErr:
		c.pendingErr = nil
		return true
	default:
		return false
	}
}

func (c *Client) deliverToWaiter(cmd RawCommand) bool {
	c.waitersLock.Lock()
	defer c.waitersLock.Unlock()
	list := c.waiters[cmd.Code]
	if len(list) == 0 {
		return false
	}
	for _, ch := range list {
		ch <- cmd.Payload
	}
	delete(c.waiters, cmd.Code)
	return true
}

// Request sends a command and waits for a specific response command.
func (c *Client) Request(ctx context.Context, code string, payload any, responseCode string, into any) error {
	ch := make(chan json.RawMessage, 1)
	c.waitersLock.Lock()
	c.waiters[responseCode] = append(c.waiters[responseCode], ch)
	c.waitersLock.Unlock()
	removeWaiter := func() {
		c.waitersLock.Lock()
		defer c.waitersLock.Unlock()
		list := c.waiters[responseCode]
		for i, item := range list {
			if item == ch {
				c.waiters[responseCode] = append(list[:i], list[i+1:]...)
				break
			}
		}
	}
	if err := c.Send(ctx, code, payload); err != nil {
		removeWaiter()
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case data := <-ch:
		if into == nil {
			return nil
		}
		return json.Unmarshal(data, into)
	case <-ctx.Done():
		removeWaiter()
		return fmt.Errorf("timed out waiting for %s response", responseCode)
	}
}

// Var returns a raw server variable (VAR command).
func (c *Client) Var(name string) (json.RawMessage, bool) {
	c.varsLock.RLock()
	defer c.varsLock.RUnlock()
	v, ok := c.vars[name]
	return v, ok
}

func (c *Client) numberVar(name string, def float64) float64 {
	raw, ok := c.Var(name)
	if !ok {
		return def
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f
	}
	// Some values (like permissions) are sent as strings.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if parsed, err := strconv.ParseFloat(s, 64); err == nil {
			return parsed
		}
	}
	return def
}

// ChatMax is the maximum length of a channel message (MSG) in bytes.
func (c *Client) ChatMax() int {
	return int(c.numberVar("chat_max", 4096))
}

// PrivMax is the maximum length of a private message (PRI) in bytes.
func (c *Client) PrivMax() int {
	return int(c.numberVar("priv_max", 50000))
}

// AdMax is the maximum length of a roleplay ad (LRP) in bytes.
func (c *Client) AdMax() int {
	return int(c.numberVar("lfrp_max", 50000))
}

// MessageFloodInterval is the minimum time between two messages.
func (c *Client) MessageFloodInterval() time.Duration {
	secs := c.numberVar("msg_flood", 0.5)
	// Add a small margin to account for latency jitter.
	return time.Duration(secs*float64(time.Second)) + 100*time.Millisecond
}
