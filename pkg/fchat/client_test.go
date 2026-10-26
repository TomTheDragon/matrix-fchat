package fchat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

// fakeServer simulates the F-List ticket API and the F-Chat websocket server.
type fakeServer struct {
	t        *testing.T
	http     *httptest.Server
	received chan RawCommand
	toClient chan string
	tickets  atomic.Int32
}

func newFakeServer(t *testing.T) *fakeServer {
	fs := &fakeServer{
		t:        t,
		received: make(chan RawCommand, 100),
		toClient: make(chan string, 100),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/getApiTicket.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		if r.PostForm.Get("password") != "secret" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "Login failed."})
			return
		}
		fs.tickets.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ticket":            "fct_test",
			"characters":        []string{"Test Char", "Other Char"},
			"default_character": "Test Char",
			"error":             "",
		})
	})
	mux.HandleFunc("/chat2", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept failed: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		go func() {
			for {
				select {
				case msg := <-fs.toClient:
					if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			cmd, err := ParseCommand(data)
			if err != nil {
				t.Errorf("client sent invalid command: %q", data)
				return
			}
			if cmd.Code == CmdIdentify {
				var idn IdentifyRequest
				_ = json.Unmarshal(cmd.Payload, &idn)
				if idn.Ticket != "fct_test" || idn.Method != "ticket" || idn.Character != "Test Char" {
					fs.toClient <- `ERR {"number":4,"message":"Identification failed."}`
				} else {
					fs.toClient <- `VAR {"variable":"msg_flood","value":0.1}`
					fs.toClient <- `VAR {"variable":"chat_max","value":4096}`
					fs.toClient <- `IDN {"character":"Test Char"}`
				}
			}
			fs.received <- cmd
		}
	})
	fs.http = httptest.NewServer(mux)
	t.Cleanup(fs.http.Close)
	return fs
}

func (fs *fakeServer) expect(code string) RawCommand {
	fs.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case cmd := <-fs.received:
			if cmd.Code == code {
				return cmd
			}
		case <-timeout:
			fs.t.Fatalf("timed out waiting for client to send %s", code)
		}
	}
}

func newTestClient(fs *fakeServer, password string) *Client {
	api := &API{SiteURL: fs.http.URL}
	return NewClient(ClientConfig{
		ServerURL:     "ws" + strings.TrimPrefix(fs.http.URL, "http") + "/chat2",
		Account:       "testaccount",
		Password:      password,
		Character:     "Test Char",
		ClientName:    "test",
		ClientVersion: "1",
		Tickets:       NewTicketCache(api),
		ErrorWait:     300 * time.Millisecond,
	}, zerolog.Nop())
}

func TestClientSession(t *testing.T) {
	fs := newFakeServer(t)
	client := newTestClient(fs, "secret")
	connected := make(chan struct{}, 1)
	handled := make(chan RawCommand, 10)
	client.OnConnected = func() { connected <- struct{}{} }
	client.Handler = func(cmd RawCommand) { handled <- cmd }
	client.Start()
	defer client.Stop()

	idn := fs.expect(CmdIdentify)
	var req IdentifyRequest
	if err := json.Unmarshal(idn.Payload, &req); err != nil || req.Account != "testaccount" {
		t.Fatalf("unexpected IDN payload: %s", idn.Payload)
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not report connection")
	}
	if !client.IsConnected() {
		t.Fatal("IsConnected should be true")
	}
	if client.ChatMax() != 4096 {
		t.Errorf("unexpected chat_max %d", client.ChatMax())
	}

	// Pings must be answered.
	fs.toClient <- "PIN"
	fs.expect(CmdPing)

	// Other commands are passed to the handler.
	fs.toClient <- `MSG {"channel":"Frontpage","character":"Someone","message":"hi"}`
	gotMsg := false
	for !gotMsg {
		select {
		case cmd := <-handled:
			// VAR commands are also passed through, skip them.
			gotMsg = cmd.Code == CmdMessage
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not receive MSG")
		}
	}

	// A message without error response succeeds after the error wait.
	ctx := context.Background()
	start := time.Now()
	if err := client.SendMessage(ctx, CmdMessage, &ChannelMessage{Channel: "Frontpage", Message: "hello"}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Error("SendMessage returned before the error wait elapsed")
	}
	sent := fs.expect(CmdMessage)
	if !strings.Contains(string(sent.Payload), `"hello"`) {
		t.Errorf("unexpected MSG payload %s", sent.Payload)
	}

	// An ERR right after sending is returned as the send error, not passed to the handler.
	go func() {
		fs.expect(CmdPrivate)
		fs.toClient <- `ERR {"number":6,"message":"The character requested was not found."}`
	}()
	err := client.SendMessage(ctx, CmdPrivate, &PrivateMessageRequest{Recipient: "Nobody", Message: "hi"})
	var fcErr *Error
	if !errors.As(err, &fcErr) || fcErr.Code != ErrCodeCharacterNotFound {
		t.Fatalf("expected error 6, got %v", err)
	}

	// Request/response commands.
	go func() {
		fs.expect(CmdPublicChannels)
		fs.toClient <- `CHA {"channels":[{"name":"Frontpage","mode":"chat","characters":100}]}`
	}()
	var list PublicChannelList
	if err := client.Request(ctx, CmdPublicChannels, nil, CmdPublicChannels, &list); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(list.Channels) != 1 || list.Channels[0].Name != "Frontpage" {
		t.Errorf("unexpected channel list %+v", list)
	}

	// Character search: results, and an error code instead of results.
	searchErrs := []int{ErrCodeNoSearchResults, ErrCodeTooManyResults, ErrCodeSearchThrottled}
	go func() {
		cmd := fs.expect(CmdSearch)
		if !strings.Contains(string(cmd.Payload), `"kinks":["523"]`) || !strings.Contains(string(cmd.Payload), `"genders":["Male"]`) {
			t.Errorf("unexpected FKS payload %s", cmd.Payload)
		}
		fs.toClient <- `FKS {"characters":["Some Guy","Another Guy"],"kinks":["523"]}`
	}()
	var result SearchResult
	err = client.RequestOrError(ctx, CmdSearch, &SearchRequest{Kinks: []string{"523"}, Genders: []string{"Male"}}, CmdSearch, &result, searchErrs...)
	if err != nil || len(result.Characters) != 2 {
		t.Fatalf("unexpected search result %+v, %v", result, err)
	}
	go func() {
		fs.expect(CmdSearch)
		fs.toClient <- `ERR {"number":18,"message":"No characters were found."}`
	}()
	err = client.RequestOrError(ctx, CmdSearch, &SearchRequest{Kinks: []string{"1"}}, CmdSearch, &result, searchErrs...)
	if !errors.As(err, &fcErr) || fcErr.Code != ErrCodeNoSearchResults {
		t.Fatalf("expected error 18, got %v", err)
	}
}

func TestClientFatalError(t *testing.T) {
	fs := newFakeServer(t)
	client := newTestClient(fs, "secret")
	fatal := make(chan error, 1)
	client.OnFatal = func(err error) { fatal <- err }
	client.Start()
	defer client.Stop()
	fs.expect(CmdIdentify)
	fs.toClient <- `ERR {"number":31,"message":"You have been disconnected because this character has been logged in at another location."}`
	select {
	case err := <-fatal:
		var fcErr *Error
		if !errors.As(err, &fcErr) || fcErr.Code != ErrCodeLoggedInElsewhere {
			t.Errorf("unexpected fatal error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected fatal error")
	}
	if client.IsRunning() {
		t.Error("client should not keep running after a fatal error")
	}
}

func TestClientBadPassword(t *testing.T) {
	fs := newFakeServer(t)
	client := newTestClient(fs, "wrong")
	fatal := make(chan error, 1)
	client.OnFatal = func(err error) { fatal <- err }
	client.Start()
	defer client.Stop()
	select {
	case err := <-fatal:
		var fe *FatalError
		if !errors.As(err, &fe) || !fe.BadCredentials || !errors.Is(err, ErrInvalidLogin) {
			t.Errorf("unexpected error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected fatal error")
	}
}

func TestTicketCacheSharing(t *testing.T) {
	fs := newFakeServer(t)
	cache := NewTicketCache(&API{SiteURL: fs.http.URL})
	ctx := context.Background()
	for range 3 {
		if _, err := cache.Get(ctx, "TestAccount", "secret", false); err != nil {
			t.Fatal(err)
		}
	}
	if n := fs.tickets.Load(); n != 1 {
		t.Errorf("expected 1 ticket request, got %d", n)
	}
	if _, err := cache.Get(ctx, "testaccount", "secret", true); err != nil {
		t.Fatal(err)
	}
	if n := fs.tickets.Load(); n != 2 {
		t.Errorf("expected 2 ticket requests after forceNew, got %d", n)
	}
}

func TestParseAndSerialize(t *testing.T) {
	cmd, err := ParseCommand([]byte("PIN"))
	if err != nil || cmd.Code != "PIN" || cmd.Payload != nil {
		t.Errorf("unexpected parse result %+v, %v", cmd, err)
	}
	data, err := Serialize("JCH", &ChannelRequest{Channel: "Frontpage"})
	if err != nil || string(data) != `JCH {"channel":"Frontpage"}` {
		t.Errorf("unexpected serialization %q, %v", data, err)
	}
	var e Error
	if err = json.Unmarshal([]byte(`{"code":40,"message":"kicked"}`), &e); err != nil || e.Code != 40 {
		t.Errorf("failed to parse alternative ERR format: %+v %v", e, err)
	}
}
