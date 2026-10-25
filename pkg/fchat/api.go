package fchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultServerURL = "wss://chat.f-list.net/chat2"
	DefaultSiteURL   = "https://www.f-list.net"
	DefaultStaticURL = "https://static.f-list.net"

	// TicketLifetime is how long an API ticket stays valid after being issued.
	TicketLifetime = 30 * time.Minute
)

// ErrInvalidLogin is returned when the F-List API rejects the account/password combination.
var ErrInvalidLogin = errors.New("invalid F-List account name or password")

// APIError is a non-empty "error" field in an F-List JSON API response.
type APIError struct {
	Endpoint string
	Message  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("F-List API error from %s: %s", e.Endpoint, e.Message)
}

type Friend struct {
	DestName   string `json:"dest_name"`
	SourceName string `json:"source_name"`
}

type Bookmark struct {
	Name string `json:"name"`
}

// TicketResponse is the response of /json/getApiTicket.php (old character list format).
type TicketResponse struct {
	Ticket           string     `json:"ticket"`
	Characters       []string   `json:"characters"`
	DefaultCharacter string     `json:"default_character"`
	Friends          []Friend   `json:"friends"`
	Bookmarks        []Bookmark `json:"bookmarks"`
	Error            string     `json:"error"`
}

// API is a minimal client for the F-List JSON API.
type API struct {
	SiteURL   string
	UserAgent string
	HTTP      *http.Client
}

func (api *API) post(ctx context.Context, endpoint string, form url.Values, into any) error {
	base := api.SiteURL
	if base == "" {
		base = DefaultSiteURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if api.UserAgent != "" {
		req.Header.Set("User-Agent", api.UserAgent)
	}
	cli := api.HTTP
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to read response from %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d from %s", resp.StatusCode, endpoint)
	}
	if err = json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("failed to parse response from %s: %w", endpoint, err)
	}
	return nil
}

// GetTicket requests a new API ticket. Issuing a ticket invalidates all
// previous tickets of the account, so callers should share tickets (see TicketCache).
func (api *API) GetTicket(ctx context.Context, account, password string) (*TicketResponse, error) {
	form := url.Values{
		"account":  {account},
		"password": {password},
	}
	var resp TicketResponse
	if err := api.post(ctx, "/json/getApiTicket.php", form, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		lower := strings.ToLower(resp.Error)
		if strings.Contains(lower, "login failed") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "no account") || strings.Contains(lower, "account name") {
			return nil, fmt.Errorf("%w: %s", ErrInvalidLogin, resp.Error)
		}
		return nil, &APIError{Endpoint: "getApiTicket.php", Message: resp.Error}
	}
	if resp.Ticket == "" {
		return nil, &APIError{Endpoint: "getApiTicket.php", Message: "response did not contain a ticket"}
	}
	return &resp, nil
}

type cachedTicket struct {
	lock     sync.Mutex
	resp     *TicketResponse
	issuedAt time.Time
}

// TicketCache shares API tickets between all connections of the same account.
// This is needed because requesting a new ticket invalidates the previous one,
// so two characters of one account logging in at the same time would otherwise
// break each other's identification.
type TicketCache struct {
	API *API

	lock    sync.Mutex
	tickets map[string]*cachedTicket
}

func NewTicketCache(api *API) *TicketCache {
	return &TicketCache{API: api, tickets: make(map[string]*cachedTicket)}
}

// Get returns a valid ticket for the account. If forceNew is set, a new ticket
// is requested even if the cached one hasn't expired yet.
func (tc *TicketCache) Get(ctx context.Context, account, password string, forceNew bool) (*TicketResponse, error) {
	key := strings.ToLower(account)
	tc.lock.Lock()
	entry, ok := tc.tickets[key]
	if !ok {
		entry = &cachedTicket{}
		tc.tickets[key] = entry
	}
	tc.lock.Unlock()

	entry.lock.Lock()
	defer entry.lock.Unlock()
	// Keep a safety margin so the ticket doesn't expire between fetching and identifying.
	if !forceNew && entry.resp != nil && time.Since(entry.issuedAt) < TicketLifetime-5*time.Minute {
		return entry.resp, nil
	}
	resp, err := tc.API.GetTicket(ctx, account, password)
	if err != nil {
		return nil, err
	}
	entry.resp = resp
	entry.issuedAt = time.Now()
	return resp, nil
}

// Invalidate drops the cached ticket for the account if it matches the given ticket.
func (tc *TicketCache) Invalidate(account, ticket string) {
	tc.lock.Lock()
	entry, ok := tc.tickets[strings.ToLower(account)]
	tc.lock.Unlock()
	if !ok {
		return
	}
	entry.lock.Lock()
	if entry.resp != nil && entry.resp.Ticket == ticket {
		entry.resp = nil
	}
	entry.lock.Unlock()
}

// AvatarURL returns the URL of a character's avatar image.
func AvatarURL(staticURL, character string) string {
	if staticURL == "" {
		staticURL = DefaultStaticURL
	}
	return fmt.Sprintf("%s/images/avatar/%s.png", strings.TrimRight(staticURL, "/"), url.PathEscape(strings.ToLower(character)))
}

// EIconURL returns the URL of an eicon image.
func EIconURL(staticURL, name string) string {
	if staticURL == "" {
		staticURL = DefaultStaticURL
	}
	return fmt.Sprintf("%s/images/eicon/%s.gif", strings.TrimRight(staticURL, "/"), url.PathEscape(strings.ToLower(name)))
}

// ProfileURL returns the URL of a character's profile page.
func ProfileURL(siteURL, character string) string {
	if siteURL == "" {
		siteURL = DefaultSiteURL
	}
	return fmt.Sprintf("%s/c/%s", strings.TrimRight(siteURL, "/"), url.PathEscape(character))
}
