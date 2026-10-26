package connector

import (
	"context"
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/fchat"
)

// The "find partners" search (FKS) finds online characters by kinks and profile fields.
// Kinks are sent as IDs, so the bridge loads the kink names from F-List's mapping list.

const (
	searchOptionsMaxAge = 24 * time.Hour
	maxSearchKinks      = 5
)

type searchKink struct {
	ID   string
	Name string
}

// searchField is a search parameter other than kinks.
type searchField struct {
	Key      string
	Aliases  []string
	ListName string // the name of the field in the mapping list's listitems
	Label    string
	get      func(req *fchat.SearchRequest) *[]string
}

var searchFields = []*searchField{
	{Key: "genders", Aliases: []string{"gender"}, ListName: "gender", Label: "Genders",
		get: func(r *fchat.SearchRequest) *[]string { return &r.Genders }},
	{Key: "orientations", Aliases: []string{"orientation"}, ListName: "orientation", Label: "Orientations",
		get: func(r *fchat.SearchRequest) *[]string { return &r.Orientations }},
	{Key: "languages", Aliases: []string{"language"}, ListName: "languagepreference", Label: "Languages",
		get: func(r *fchat.SearchRequest) *[]string { return &r.Languages }},
	{Key: "furryprefs", Aliases: []string{"furrypref", "furry"}, ListName: "furrypref", Label: "Furry preferences",
		get: func(r *fchat.SearchRequest) *[]string { return &r.FurryPrefs }},
	{Key: "roles", Aliases: []string{"role", "subdom"}, ListName: "subdom", Label: "Dom/sub roles",
		get: func(r *fchat.SearchRequest) *[]string { return &r.Roles }},
	{Key: "positions", Aliases: []string{"position"}, ListName: "position", Label: "Positions",
		get: func(r *fchat.SearchRequest) *[]string { return &r.Positions }},
}

type searchOptions struct {
	Kinks  []searchKink
	Values map[string][]string // searchField.Key -> allowed values
	// ListItems maps list item IDs to their values (for profile infotags).
	ListItems map[string]string
	fetched   time.Time
}

type searchOptionsCache struct {
	lock    sync.Mutex
	options *searchOptions
}

// getSearchOptions returns the kinks and field values, loading them from F-List once a day.
func (fc *FChatConnector) getSearchOptions(ctx context.Context) (*searchOptions, error) {
	fc.searchOptions.lock.Lock()
	defer fc.searchOptions.lock.Unlock()
	cached := fc.searchOptions.options
	if cached != nil && time.Since(cached.fetched) < searchOptionsMaxAge {
		return cached, nil
	}
	mapping, err := fc.API.GetMappingList(ctx)
	if err != nil {
		if cached != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to refresh search options, using old ones")
			return cached, nil
		}
		return nil, err
	}
	opts := &searchOptions{Values: make(map[string][]string), fetched: time.Now()}
	for _, kink := range mapping.Kinks {
		if kink.ID != "" && kink.Name != "" {
			opts.Kinks = append(opts.Kinks, searchKink{ID: kink.ID, Name: html.UnescapeString(kink.Name)})
		}
	}
	slices.SortFunc(opts.Kinks, func(a, b searchKink) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	opts.ListItems = make(map[string]string, len(mapping.ListItems))
	for _, item := range mapping.ListItems {
		opts.ListItems[item.ID] = html.UnescapeString(item.Value)
		for _, field := range searchFields {
			if item.Name == field.ListName {
				opts.Values[field.Key] = append(opts.Values[field.Key], html.UnescapeString(item.Value))
			}
		}
	}
	fc.searchOptions.options = opts
	return opts, nil
}

func normalizeSearchText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// matchOne finds the value matching the input exactly, or the only value containing it.
func matchOne(input string, values []string) (string, error) {
	norm := normalizeSearchText(input)
	if norm == "" {
		return "", errors.New("empty value")
	}
	var partial []string
	for _, value := range values {
		v := normalizeSearchText(value)
		if v == norm {
			return value, nil
		} else if strings.Contains(v, norm) {
			partial = append(partial, value)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		return "", fmt.Errorf("unknown value %q", input)
	}
	if len(partial) > 8 {
		partial = append(partial[:8], "…")
	}
	return "", fmt.Errorf("%q matches several values: %s", input, strings.Join(partial, ", "))
}

// matchList matches a comma separated list of values. Values can contain commas themselves
// (e.g. "Robots, Cyborgs, Androids"), so the longest exact match is tried first.
func matchList(input string, values []string) ([]string, error) {
	tokens := strings.Split(input, ",")
	exact := make(map[string]string, len(values))
	for _, value := range values {
		exact[normalizeSearchText(value)] = value
	}
	var out []string
	for i := 0; i < len(tokens); {
		matched := false
		for j := len(tokens); j > i+1; j-- {
			if value, ok := exact[normalizeSearchText(strings.Join(tokens[i:j], ","))]; ok {
				out = append(out, value)
				i, matched = j, true
				break
			}
		}
		if matched {
			continue
		}
		if strings.TrimSpace(tokens[i]) != "" {
			value, err := matchOne(tokens[i], values)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		i++
	}
	return slices.Compact(out), nil
}

type searchQuery struct {
	Request fchat.SearchRequest
	// KinkNames are the names of the requested kinks, for showing the query.
	KinkNames []string
	Limit     int
	// Text is the query without the limit, to suggest showing more results.
	Text string
}

// parseSearchQuery parses "kinks: a, b; genders: x; limit: 20".
func parseSearchQuery(input string, opts *searchOptions) (*searchQuery, error) {
	query := &searchQuery{Limit: defaultListedContacts}
	query.Request.Kinks = []string{}
	var textParts []string
	kinkNames := make([]string, len(opts.Kinks))
	kinkIDs := make(map[string]string, len(opts.Kinks))
	for i, kink := range opts.Kinks {
		kinkNames[i] = kink.Name
		kinkIDs[normalizeSearchText(kink.Name)] = kink.ID
	}
	sections := strings.FieldsFunc(input, func(r rune) bool { return r == ';' || r == '\n' })
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		rawKey, value, ok := strings.Cut(section, ":")
		if !ok {
			return nil, fmt.Errorf("missing `:` in %q", strings.TrimSpace(section))
		}
		key := strings.ToLower(strings.TrimSpace(rawKey))
		value = strings.TrimSpace(value)
		switch key {
		case "kinks", "kink":
			names, err := matchList(value, kinkNames)
			if err != nil {
				return nil, fmt.Errorf("kinks: %w", err)
			}
			for _, name := range names {
				query.KinkNames = append(query.KinkNames, name)
				query.Request.Kinks = append(query.Request.Kinks, kinkIDs[normalizeSearchText(name)])
			}
			query.KinkNames = slices.Compact(query.KinkNames)
			query.Request.Kinks = slices.Compact(query.Request.Kinks)
		case "limit", "show", "count":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("limit must be a positive number")
			}
			query.Limit = min(n, maxListedContacts)
			continue
		default:
			field := findSearchField(key)
			if field == nil {
				return nil, fmt.Errorf("unknown search field %q", strings.TrimSpace(rawKey))
			}
			matched, err := matchList(value, opts.Values[field.Key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", field.Key, err)
			}
			target := field.get(&query.Request)
			*target = slices.Compact(append(*target, matched...))
		}
		textParts = append(textParts, strings.TrimSpace(section))
	}
	if len(query.Request.Kinks) == 0 {
		return nil, errors.New("at least one kink is required")
	} else if len(query.Request.Kinks) > maxSearchKinks {
		return nil, fmt.Errorf("at most %d kinks can be searched at once", maxSearchKinks)
	}
	query.Text = strings.Join(textParts, "; ")
	return query, nil
}

func findSearchField(key string) *searchField {
	for _, field := range searchFields {
		if field.Key == key || slices.Contains(field.Aliases, key) {
			return field
		}
	}
	return nil
}

func (q *searchQuery) describe() string {
	parts := []string{"kinks: " + strings.Join(q.KinkNames, ", ")}
	for _, field := range searchFields {
		if values := *field.get(&q.Request); len(values) > 0 {
			parts = append(parts, field.Key+": "+strings.Join(values, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

const searchUsage = "**Usage:** `$cmdprefix find-partners kinks: <kink>, <kink>; genders: <gender>; … [; limit: <number>]`\n\n" +
	"Finds online characters. At least one kink is required (at most 5). " +
	"Values can be shortened as long as they're unique. " +
	"Example: `$cmdprefix find-partners kinks: dirty talk, femboys; genders: male, cunt-boy; roles: switch; limit: 20`\n\n" +
	"Find kink names with `$cmdprefix find-partners kinks <search text>`."

func (fc *FChatConnector) searchHelp(opts *searchOptions) string {
	var out strings.Builder
	out.WriteString(searchUsage + "\n\nOther fields (optional):\n\n")
	for _, field := range searchFields {
		fmt.Fprintf(&out, "* `%s`: %s\n", field.Key, strings.Join(opts.Values[field.Key], ", "))
	}
	return out.String()
}

const maxListedKinks = 50

func (fc *FChatConnector) listKinks(ce *commands.Event, opts *searchOptions, filter string) {
	filter = normalizeSearchText(filter)
	var names []string
	for _, kink := range opts.Kinks {
		if strings.Contains(normalizeSearchText(kink.Name), filter) {
			names = append(names, kink.Name)
		}
	}
	if len(names) == 0 {
		ce.Reply("No kinks found")
		return
	}
	header := fmt.Sprintf("Found %d kinks", len(names))
	if len(names) > maxListedKinks {
		header += fmt.Sprintf(" (showing the first %d)", maxListedKinks)
		names = names[:maxListedKinks]
	}
	ce.Reply("%s:\n\n%s", header, strings.Join(names, " · "))
}

func (fc *FChatConnector) cmdFindPartners(ce *commands.Event) {
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	opts, err := fc.getSearchOptions(ce.Ctx)
	if err != nil {
		ce.Reply("Failed to load the kink list from F-List: %v", err)
		return
	}
	raw := strings.TrimSpace(ce.RawArgs)
	if raw == "" || strings.EqualFold(raw, "help") {
		ce.Reply(fc.searchHelp(opts))
		return
	}
	if !strings.Contains(raw, ":") && len(ce.Args) > 0 && strings.EqualFold(ce.Args[0], "kinks") {
		fc.listKinks(ce, opts, strings.TrimSpace(raw[len(ce.Args[0]):]))
		return
	}
	query, err := parseSearchQuery(raw, opts)
	if err != nil {
		ce.Reply("Invalid search: %v\n\n%s", err, searchUsage)
		return
	}

	var result fchat.SearchResult
	err = client.Conn.RequestOrError(ce.Ctx, fchat.CmdSearch, &query.Request, fchat.CmdSearch, &result,
		fchat.ErrCodeNoSearchResults, fchat.ErrCodeTooManyResults, fchat.ErrCodeSearchThrottled)
	var fcErr *fchat.Error
	switch {
	case errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeNoSearchResults:
		ce.Reply("No online characters match your search (%s)", query.describe())
		return
	case errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeTooManyResults:
		ce.Reply("Too many characters match your search, please add more filters")
		return
	case errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeSearchThrottled:
		ce.Reply("F-Chat only allows one search every few seconds, please try again in a moment")
		return
	case err != nil:
		ce.Reply("Search failed: %v", err)
		return
	}

	found := make(map[networkid.UserID]string, len(result.Characters))
	for _, name := range result.Characters {
		userID := makeUserID(name)
		if userID == "" || userID == client.ownID || client.isIgnored(name) {
			continue
		}
		found[userID] = name
	}
	if len(found) == 0 {
		ce.Reply("No online characters match your search (%s)", query.describe())
		return
	}
	entries := client.contactEntries(found, "")
	total, online := len(entries), len(entries)
	if total > query.Limit {
		entries = entries[:query.Limit]
	}
	client.loadAvatars(ce.Ctx, entries)
	usage := fmt.Sprintf("%s find-partners %s; limit: %d", fc.Bridge.Config.CommandPrefix, query.Text, min(total, maxListedContacts))
	content := fc.renderContactList("Partner search: "+query.describe(), entries, total, online, usage)
	_, err = ce.Bot.SendMessage(ce.Ctx, ce.OrigRoomID, event.EventMessage, &event.Content{Parsed: content}, nil)
	if err != nil {
		ce.Log.Err(err).Msg("Failed to send search results")
	}
}
