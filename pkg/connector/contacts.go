package connector

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

// fetchContactLists loads the friend list of the character and the bookmarks of the account
// from the F-List API. If that fails, the (possibly older) lists from the API ticket are used.
func (c *FChatClient) fetchContactLists(ctx context.Context) error {
	c.metaLock.Lock()
	account, password, character := c.meta().Account, c.meta().Password, c.meta().Character
	c.metaLock.Unlock()

	ticket, err := c.Main.Tickets.Get(ctx, account, password, false)
	if err != nil {
		return fmt.Errorf("failed to get API ticket: %w", err)
	}
	lists, err := c.Main.API.GetFriendsAndBookmarks(ctx, account, ticket.Ticket)
	if fchat.IsInvalidTicket(err) {
		c.Main.Tickets.Invalidate(account, ticket.Ticket)
		ticket, err = c.Main.Tickets.Get(ctx, account, password, true)
		if err != nil {
			return fmt.Errorf("failed to get API ticket: %w", err)
		}
		lists, err = c.Main.API.GetFriendsAndBookmarks(ctx, account, ticket.Ticket)
	}
	var friends, bookmarks []string
	if err != nil {
		c.log.Warn().Err(err).Msg("Failed to fetch friend and bookmark lists, using the lists from the API ticket")
		friends = fchat.FriendsOf(character, ticket.FriendEntries())
		bookmarks = ticket.BookmarkNames()
	} else {
		friends = fchat.FriendsOf(character, lists.Friends)
		bookmarks = lists.Bookmarks
	}

	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	fillNameMap(c.friends, friends)
	fillNameMap(c.bookmarks, bookmarks)
	c.listsFetched = time.Now()
	return nil
}

func fillNameMap(target map[networkid.UserID]string, names []string) {
	clear(target)
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			target[makeUserID(name)] = name
		}
	}
}

// refreshContactLists fetches the lists unless they were fetched less than maxAge ago.
func (c *FChatClient) refreshContactLists(ctx context.Context, maxAge time.Duration) error {
	c.listsLock.Lock()
	defer c.listsLock.Unlock()
	c.stateLock.RLock()
	fresh := !c.listsFetched.IsZero() && time.Since(c.listsFetched) < maxAge
	c.stateLock.RUnlock()
	if fresh {
		return nil
	}
	return c.fetchContactLists(ctx)
}

// syncContactLists runs after connecting.
func (c *FChatClient) syncContactLists(ctx context.Context) {
	// F-List asks clients not to poll the list endpoint, so frequent reconnects reuse the lists.
	if err := c.refreshContactLists(ctx, 10*time.Minute); err != nil {
		c.log.Err(err).Msg("Failed to sync friend and bookmark lists")
		return
	}
	if c.Main.Config.CreateFriendDMs {
		c.stateLock.RLock()
		friends := make([]string, 0, len(c.friends))
		for _, name := range c.friends {
			friends = append(friends, name)
		}
		c.stateLock.RUnlock()
		slices.SortFunc(friends, func(a, b string) int {
			return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
		})
		for _, name := range friends {
			if c.createFriendDM(ctx, name) {
				// Don't create lots of rooms at the same moment.
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
}

// createFriendDM creates the DM room of a friend unless the bridge already did that before.
// It returns whether a room creation was queued.
func (c *FChatClient) createFriendDM(ctx context.Context, name string) bool {
	userID := makeUserID(name)
	if userID == "" || userID == c.ownID {
		return false
	}
	c.metaLock.Lock()
	done := c.meta().FriendDMs[string(userID)]
	c.metaLock.Unlock()
	if done {
		return false
	}
	portalKey := makeDMPortalKey(name, c.UserLogin.ID)
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		c.log.Err(err).Str("friend", name).Msg("Failed to check for existing DM portal")
		return false
	}
	queued := false
	if portal == nil || portal.MXID == "" {
		c.log.Debug().Str("friend", name).Msg("Creating DM room for friend")
		c.Main.Bridge.QueueRemoteEvent(c.UserLogin, &simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    portalKey,
				CreatePortal: true,
				LogContext: func(lc zerolog.Context) zerolog.Context {
					return lc.Str("friend", name)
				},
			},
			ChatInfo: c.dmChatInfo(userID),
		})
		queued = true
	}
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	meta := c.meta()
	if meta.FriendDMs == nil {
		meta.FriendDMs = make(map[string]bool)
	}
	meta.FriendDMs[string(userID)] = true
	if err = c.UserLogin.Save(ctx); err != nil {
		c.log.Err(err).Msg("Failed to save login after creating friend DM")
	}
	return queued
}

// updateContactList adds or removes a character after a realtime notification from the server.
func (c *FChatClient) updateContactList(list map[networkid.UserID]string, name string, add bool) {
	if name == "" {
		return
	}
	c.stateLock.Lock()
	defer c.stateLock.Unlock()
	if add {
		list[makeUserID(name)] = name
	} else {
		delete(list, makeUserID(name))
	}
}

type contactEntry struct {
	ID        networkid.UserID
	Name      string
	Status    fchat.Status
	StatusMsg string
	AvatarMXC string
}

// contactEntries returns the entries of a list matching the filter, online characters first.
func (c *FChatClient) contactEntries(list map[networkid.UserID]string, filter string) []*contactEntry {
	c.stateLock.RLock()
	entries := make([]*contactEntry, 0, len(list))
	for userID, name := range list {
		if !matchesFilter(filter, name) {
			continue
		}
		entry := &contactEntry{ID: userID, Name: name, Status: fchat.StatusOffline}
		if info, ok := c.characters[userID]; ok && info.Status != "" {
			entry.Status = info.Status
			entry.StatusMsg = info.StatusMsg
		}
		entries = append(entries, entry)
	}
	c.stateLock.RUnlock()
	slices.SortFunc(entries, func(a, b *contactEntry) int {
		aOnline, bOnline := a.Status != fchat.StatusOffline, b.Status != fchat.StatusOffline
		if aOnline != bOnline {
			if aOnline {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return entries
}

// avatarWorkers limits how many avatars are downloaded at the same time.
const avatarWorkers = 8

// loadAvatars makes sure the ghosts of the entries have their avatars uploaded to Matrix
// and stores the mxc:// URIs in the entries.
func (c *FChatClient) loadAvatars(ctx context.Context, entries []*contactEntry) {
	work := make(chan *contactEntry)
	var wg sync.WaitGroup
	for range min(avatarWorkers, len(entries)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range work {
				ghost, err := c.Main.Bridge.GetGhostByID(ctx, entry.ID)
				if err != nil {
					c.log.Err(err).Str("character", entry.Name).Msg("Failed to get ghost for avatar")
					continue
				}
				// Doesn't download anything if the avatar is already up to date.
				ghost.UpdateInfo(ctx, c.Main.makeUserInfo(entry.Name))
				entry.AvatarMXC = string(ghost.AvatarMXC)
			}
		}()
	}
	for _, entry := range entries {
		work <- entry
	}
	close(work)
	wg.Wait()
}

var statusIcons = map[fchat.Status]string{
	fchat.StatusOnline:  "🟢",
	fchat.StatusLooking: "🟢",
	fchat.StatusCrown:   "🟢",
	fchat.StatusBusy:    "🟠",
	fchat.StatusDND:     "🔴",
	fchat.StatusIdle:    "🟡",
	fchat.StatusAway:    "🟡",
	fchat.StatusOffline: "⚫",
}

const maxStatusMsgLength = 100

func (e *contactEntry) statusText() string {
	icon := statusIcons[e.Status]
	if icon == "" {
		icon = "🟢"
	}
	text := icon + " " + string(e.Status)
	if msg := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(e.StatusMsg))); msg != "" {
		if runes := []rune(msg); len(runes) > maxStatusMsgLength {
			msg = string(runes[:maxStatusMsgLength]) + "…"
		}
		text += ": " + msg
	}
	return text
}

const avatarSize = 24

// renderContactList renders the list as a notice with inline avatars.
func (fc *FChatConnector) renderContactList(title string, entries []*contactEntry, total int) *event.MessageEventContent {
	online := 0
	for _, entry := range entries {
		if entry.Status != fchat.StatusOffline {
			online++
		}
	}
	header := fmt.Sprintf("%s (%d, %d online)", title, len(entries), online)
	if total > len(entries) {
		header = fmt.Sprintf("%s (showing %d of %d, %d online)", title, len(entries), total, online)
	}

	var plain, htmlOut strings.Builder
	plain.WriteString(header + ":\n\n")
	fmt.Fprintf(&htmlOut, "<p>%s:</p>\n<ul>\n", html.EscapeString(header))
	for _, entry := range entries {
		status := entry.statusText()
		fmt.Fprintf(&plain, "* %s – %s\n", entry.Name, status)
		htmlOut.WriteString("<li>")
		if entry.AvatarMXC != "" {
			fmt.Fprintf(&htmlOut, `<img src="%s" alt="" title="%s" width="%d" height="%d"> `,
				html.EscapeString(entry.AvatarMXC), html.EscapeString(entry.Name), avatarSize, avatarSize)
		}
		fmt.Fprintf(&htmlOut, `<a href="%s"><strong>%s</strong></a> – %s</li>`+"\n",
			html.EscapeString(fchat.ProfileURL(fc.Config.SiteURL, entry.Name)),
			html.EscapeString(entry.Name), html.EscapeString(status))
	}
	htmlOut.WriteString("</ul>\n")
	startChat := fc.Bridge.Config.CommandPrefix + " start-chat <name>"
	plain.WriteString("\nStart a private chat with `" + startChat + "`")
	fmt.Fprintf(&htmlOut, "<p>Start a private chat with <code>%s</code></p>", html.EscapeString(startChat))
	return &event.MessageEventContent{
		MsgType:       event.MsgNotice,
		Body:          plain.String(),
		Format:        event.FormatHTML,
		FormattedBody: htmlOut.String(),
		Mentions:      &event.Mentions{},
	}
}

const maxListedContacts = 100

func (fc *FChatConnector) cmdBookmarks(ce *commands.Event) {
	fc.listContacts(ce, "Your bookmarks", func(c *FChatClient) map[networkid.UserID]string { return c.bookmarks })
}

func (fc *FChatConnector) cmdFriends(ce *commands.Event) {
	fc.listContacts(ce, "Your friends", func(c *FChatClient) map[networkid.UserID]string { return c.friends })
}

func (fc *FChatConnector) listContacts(ce *commands.Event, title string, getList func(*FChatClient) map[networkid.UserID]string) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	if err := client.refreshContactLists(ce.Ctx, time.Minute); err != nil {
		client.stateLock.RLock()
		fetched := !client.listsFetched.IsZero()
		client.stateLock.RUnlock()
		if !fetched {
			ce.Reply("Failed to get the list from F-List: %v", err)
			return
		}
		ce.Log.Warn().Err(err).Msg("Failed to refresh contact lists, showing cached lists")
	}
	filter := strings.ToLower(strings.TrimSpace(ce.RawArgs))
	entries := client.contactEntries(getList(client), filter)
	if len(entries) == 0 {
		if filter != "" {
			ce.Reply("No matching characters found")
		} else {
			ce.Reply("The list is empty")
		}
		return
	}
	total := len(entries)
	if total > maxListedContacts {
		entries = entries[:maxListedContacts]
	}
	client.loadAvatars(ce.Ctx, entries)
	content := fc.renderContactList(title, entries, total)
	_, err := ce.Bot.SendMessage(ce.Ctx, ce.OrigRoomID, event.EventMessage, &event.Content{Parsed: content}, nil)
	if err != nil {
		ce.Log.Err(err).Msg("Failed to send contact list")
	}
}
