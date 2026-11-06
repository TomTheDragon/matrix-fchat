package connector

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

// dmPartnerName returns the name of the character the DM portal the command was sent in is
// with, or "" outside of DM rooms and in the broadcast chat.
func (fc *FChatConnector) dmPartnerName(ce *commands.Event, client *FChatClient) string {
	if ce.Portal == nil {
		return ""
	}
	_, dmUser, ok := parsePortalID(ce.Portal.ID)
	if !ok || dmUser == "" || dmUser == broadcastUserID {
		return ""
	}
	if name := client.characterName(dmUser); name != "" {
		return name
	}
	if ghost, _ := fc.Bridge.GetExistingGhostByID(ce.Ctx, dmUser); ghost != nil && fc.ghostCharacterName(ghost) != "" {
		return fc.ghostCharacterName(ghost)
	}
	return string(dmUser)
}

// replyAPIError reports a failed F-List API request.
func replyAPIError(ce *commands.Event, action string, err error) {
	var apiErr *fchat.APIError
	if errors.As(err, &apiErr) {
		ce.Reply("Failed to %s: %s", action, apiErr.Message)
	} else {
		ce.Reply("Failed to %s: %v", action, err)
	}
}

// splitAction splits "<action> <rest>" and lowercases the action.
func splitAction(ce *commands.Event) (action, rest string) {
	if len(ce.Args) == 0 {
		return "", ""
	}
	action = strings.ToLower(ce.Args[0])
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ce.RawArgs), ce.Args[0]))
	return action, rest
}

func (fc *FChatConnector) cmdFriend(ce *commands.Event) {
	const usage = "**Usage:** `$cmdprefix friend <add|remove|accept|deny|cancel> [character]` " +
		"(in a private chat, the character is optional). `$cmdprefix friend-requests` lists open requests."
	action, name := splitAction(ce)
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	if name == "" {
		name = fc.dmPartnerName(ce, client)
	}
	if action == "" || name == "" {
		ce.Reply(usage)
		return
	}
	own := client.meta().Character
	var err error
	var done string
	switch action {
	case "add", "request", "send":
		err = client.withTicket(ce.Ctx, func(account, ticket string) error {
			return client.Main.API.SendFriendRequest(ce.Ctx, account, ticket, own, name)
		})
		done = fmt.Sprintf("Sent a friend request from %s to %s", own, name)
	case "remove", "unfriend":
		err = client.withTicket(ce.Ctx, func(account, ticket string) error {
			return client.Main.API.RemoveFriend(ce.Ctx, account, ticket, own, name)
		})
		if err == nil {
			client.updateContactList(client.friends, name, false)
		}
		done = fmt.Sprintf("%s and %s are no longer friends", own, name)
	case "accept", "deny", "cancel":
		err = fc.answerFriendRequest(ce, client, action, name)
		switch action {
		case "accept":
			done = fmt.Sprintf("Accepted the friend request from %s", name)
		case "deny":
			done = fmt.Sprintf("Denied the friend request from %s", name)
		default:
			done = fmt.Sprintf("Cancelled the friend request to %s", name)
		}
	default:
		ce.Reply(usage)
		return
	}
	if err != nil {
		replyAPIError(ce, action+" the friend request", err)
		return
	}
	ce.Reply(done)
}

var errNoFriendRequest = errors.New("no matching friend request")

// answerFriendRequest accepts, denies or cancels the friend request between the command's
// character and another character.
func (fc *FChatConnector) answerFriendRequest(ce *commands.Event, client *FChatClient, action, name string) error {
	own := client.meta().Character
	return client.withTicket(ce.Ctx, func(account, ticket string) error {
		requests, err := client.Main.API.GetFriendRequests(ce.Ctx, account, ticket)
		if err != nil {
			return err
		}
		var id int64
		if action == "cancel" {
			id = findFriendRequest(requests.Outgoing, own, name, false)
		} else {
			id = findFriendRequest(requests.Incoming, own, name, true)
		}
		if id == 0 {
			if action == "cancel" {
				return fmt.Errorf("%w: %s didn't send a friend request to %s", errNoFriendRequest, own, name)
			}
			return fmt.Errorf("%w: %s has no friend request from %s", errNoFriendRequest, own, name)
		}
		switch action {
		case "accept":
			return client.Main.API.AcceptFriendRequest(ce.Ctx, account, ticket, id)
		case "deny":
			return client.Main.API.DenyFriendRequest(ce.Ctx, account, ticket, id)
		default:
			return client.Main.API.CancelFriendRequest(ce.Ctx, account, ticket, id)
		}
	})
}

// findFriendRequest returns the ID of the request between own and other. For incoming
// requests, other is the source, for outgoing requests the destination. Requests to other
// characters of the account are used if there's none for own.
func findFriendRequest(requests []fchat.FriendRequest, own, other string, incoming bool) int64 {
	var fallback int64
	for _, req := range requests {
		mine, theirs := req.Source, req.Dest
		if incoming {
			mine, theirs = req.Dest, req.Source
		}
		if !strings.EqualFold(theirs, other) {
			continue
		}
		if strings.EqualFold(mine, own) {
			return req.ID
		}
		if fallback == 0 {
			fallback = req.ID
		}
	}
	return fallback
}

func (fc *FChatConnector) cmdFriendRequests(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	var requests *fchat.FriendRequests
	err := client.withTicket(ce.Ctx, func(account, ticket string) error {
		var reqErr error
		requests, reqErr = client.Main.API.GetFriendRequests(ce.Ctx, account, ticket)
		return reqErr
	})
	if err != nil {
		replyAPIError(ce, "load the friend requests", err)
		return
	}
	ce.Reply(formatFriendRequests(requests, client.meta().Character))
}

// formatFriendRequests lists the requests as Markdown. Requests of other characters of the
// account are marked with the character's name.
func formatFriendRequests(requests *fchat.FriendRequests, own string) string {
	if len(requests.Incoming) == 0 && len(requests.Outgoing) == 0 {
		return "No open friend requests"
	}
	sortRequests := func(list []fchat.FriendRequest) {
		slices.SortFunc(list, func(a, b fchat.FriendRequest) int {
			return cmp.Compare(strings.ToLower(a.Source+"\x00"+a.Dest), strings.ToLower(b.Source+"\x00"+b.Dest))
		})
	}
	var out strings.Builder
	line := func(other, mine string) {
		fmt.Fprintf(&out, "* **%s**", other)
		if !strings.EqualFold(mine, own) {
			fmt.Fprintf(&out, " (to/from your character %s)", mine)
		}
		out.WriteString("\n")
	}
	if len(requests.Incoming) > 0 {
		sortRequests(requests.Incoming)
		fmt.Fprintf(&out, "**Incoming friend requests** (%d):\n\n", len(requests.Incoming))
		for _, req := range requests.Incoming {
			line(req.Source, req.Dest)
		}
		out.WriteString("\nAnswer with `$cmdprefix friend accept <name>` or `$cmdprefix friend deny <name>`.\n\n")
	}
	if len(requests.Outgoing) > 0 {
		sortRequests(requests.Outgoing)
		fmt.Fprintf(&out, "**Sent friend requests** (%d):\n\n", len(requests.Outgoing))
		for _, req := range requests.Outgoing {
			line(req.Dest, req.Source)
		}
		out.WriteString("\nCancel with `$cmdprefix friend cancel <name>`.\n")
	}
	return strings.TrimSpace(out.String())
}

func (fc *FChatConnector) cmdBookmark(ce *commands.Event) {
	const usage = "**Usage:** `$cmdprefix bookmark <add|remove> [character]` (in a private chat, the character is optional)"
	action, name := splitAction(ce)
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	if name == "" {
		name = fc.dmPartnerName(ce, client)
	}
	if name == "" {
		ce.Reply(usage)
		return
	}
	var err error
	switch action {
	case "add":
		err = client.withTicket(ce.Ctx, func(account, ticket string) error {
			return client.Main.API.AddBookmark(ce.Ctx, account, ticket, name)
		})
	case "remove", "delete":
		err = client.withTicket(ce.Ctx, func(account, ticket string) error {
			return client.Main.API.RemoveBookmark(ce.Ctx, account, ticket, name)
		})
	default:
		ce.Reply(usage)
		return
	}
	if err != nil {
		replyAPIError(ce, action+" the bookmark", err)
		return
	}
	// The server confirms with an RTB notification, which updates the list.
	ce.React("✅")
}

// parseMemoArgs splits "[character] [= text]".
func parseMemoArgs(raw string) (name string, set bool, text string) {
	name, text, set = strings.Cut(raw, "=")
	return strings.TrimSpace(name), set, strings.TrimSpace(text)
}

func (fc *FChatConnector) cmdMemo(ce *commands.Event) {
	const usage = "**Usage:** `$cmdprefix memo <character>` shows your memo, `$cmdprefix memo <character> = <text>` " +
		"sets it and `$cmdprefix memo <character> =` deletes it. In a private chat, the character is optional."
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	name, set, text := parseMemoArgs(ce.RawArgs)
	if name == "" {
		name = fc.dmPartnerName(ce, client)
	}
	if name == "" {
		ce.Reply(usage)
		return
	}
	if set {
		if len([]rune(text)) > fchat.MaxMemoLength {
			ce.Reply("Memos can't be longer than %d characters", fchat.MaxMemoLength)
			return
		}
		err := client.withTicket(ce.Ctx, func(account, ticket string) error {
			return client.Main.API.SaveMemo(ce.Ctx, account, ticket, name, text)
		})
		if err != nil {
			replyAPIError(ce, "save the memo", err)
		} else if text == "" {
			ce.Reply("Deleted your memo about %s", name)
		} else {
			ce.Reply("Saved your memo about %s", name)
		}
		return
	}
	var memo string
	err := client.withTicket(ce.Ctx, func(account, ticket string) error {
		var reqErr error
		memo, reqErr = client.Main.API.GetMemo(ce.Ctx, account, ticket, name)
		return reqErr
	})
	if err != nil {
		replyAPIError(ce, "load the memo", err)
		return
	}
	memo = strings.TrimSpace(bbcode.DecodeEntities(memo))
	if memo == "" {
		ce.Reply("You have no memo about %s. Add one with `$cmdprefix memo %s = <text>`", name, name)
		return
	}
	ce.Reply("**Your memo about %s:**\n\n%s", name, quoteMarkdown(memo))
}

// quoteMarkdown turns text into a Markdown quote.
func quoteMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}
