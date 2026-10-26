package connector

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func (c *FChatClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	name := strings.TrimSpace(identifier)
	name = strings.TrimPrefix(name, "fchat:")
	if !isValidCharacterName(name) {
		return nil, fmt.Errorf("%q is not a valid F-List character name", identifier)
	}
	userID := makeUserID(name)
	if userID == c.ownID {
		return nil, fmt.Errorf("can't start a chat with yourself")
	}
	if known := c.characterName(userID); known != "" {
		name = known
	}
	return c.makeResolveResponse(ctx, userID, name, createChat)
}

func (c *FChatClient) makeResolveResponse(ctx context.Context, userID networkid.UserID, name string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	ghost, err := c.Main.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	resp := &bridgev2.ResolveIdentifierResponse{
		Ghost:    ghost,
		UserID:   userID,
		UserInfo: c.userInfo(name),
	}
	if createChat {
		resp.Chat = &bridgev2.CreateChatResponse{
			PortalKey:  makeDMPortalKey(name, c.UserLogin.ID),
			PortalInfo: c.dmChatInfo(userID),
		}
	}
	return resp, nil
}

func (c *FChatClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	// Contacts are the character's friends and the account's bookmarks.
	c.stateLock.RLock()
	seen := make(map[networkid.UserID]bool, len(c.friends)+len(c.bookmarks))
	names := make([]string, 0, len(c.friends)+len(c.bookmarks))
	for _, list := range []map[networkid.UserID]string{c.friends, c.bookmarks} {
		for userID, name := range list {
			if !seen[userID] {
				seen[userID] = true
				names = append(names, name)
			}
		}
	}
	c.stateLock.RUnlock()
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	resp := make([]*bridgev2.ResolveIdentifierResponse, 0, len(names))
	for _, name := range names {
		item, err := c.makeResolveResponse(ctx, makeUserID(name), name, false)
		if err != nil {
			return nil, err
		}
		resp = append(resp, item)
	}
	return resp, nil
}

const maxSearchResults = 50

// SearchUsers searches the characters that are currently online.
func (c *FChatClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	c.stateLock.RLock()
	var exact string
	var matches []string
	for userID, info := range c.characters {
		if userID == c.ownID {
			continue
		}
		if string(userID) == query {
			exact = info.Name
		} else if strings.Contains(string(userID), query) {
			matches = append(matches, info.Name)
		}
	}
	c.stateLock.RUnlock()
	slices.SortFunc(matches, func(a, b string) int {
		// Prefer prefix matches and shorter names.
		aPrefix := strings.HasPrefix(strings.ToLower(a), query)
		bPrefix := strings.HasPrefix(strings.ToLower(b), query)
		if aPrefix != bPrefix {
			if aPrefix {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(len(a), len(b)), cmp.Compare(a, b))
	})
	if exact != "" {
		matches = append([]string{exact}, matches...)
	}
	if len(matches) > maxSearchResults {
		matches = matches[:maxSearchResults]
	}
	resp := make([]*bridgev2.ResolveIdentifierResponse, 0, len(matches))
	for _, name := range matches {
		item, err := c.makeResolveResponse(ctx, makeUserID(name), name, false)
		if err != nil {
			return nil, err
		}
		resp = append(resp, item)
	}
	return resp, nil
}
