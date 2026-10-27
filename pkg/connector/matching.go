package connector

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"matrix-fchat/pkg/fchat"
)

// find-matches compares the profiles of online characters with the user's character and
// shows the best matches. F-Chat can't search by profile similarity, so the bridge finds
// online characters with the character search (FKS) for some of the user's favorite kinks,
// loads their profiles and compares kinks, gender and orientation, language, furry
// preference, dom/sub role and position.

const (
	matchResults = 5
	// maxMatchCandidates limits how many profiles are loaded for one search.
	maxMatchCandidates = 40
	// matchSearches is the maximum number of character searches per run.
	matchSearches = 3
	// matchSearchInterval is the time between two character searches (F-Chat throttles them).
	matchSearchInterval = 5 * time.Second
	// matchFetchInterval is the time between two profile requests while matching.
	matchFetchInterval = 500 * time.Millisecond
	// matchRetries is how often a search or profile request is tried again after F-Chat or
	// F-List asked to slow down. The wait doubles with every try, starting at matchRetryWait.
	matchRetries   = 4
	matchRetryWait = 5 * time.Second
	matchTimeout   = 3 * time.Minute
	// matchSearchTimeout leaves the rest of matchTimeout for loading profiles.
	matchSearchTimeout = 90 * time.Second
	// matchProfileMaxAge is how long loaded profiles are reused for later searches.
	matchProfileMaxAge = 6 * time.Hour
	// maxMatchFavorites is how many kinks both have as favorites are shown per match.
	maxMatchFavorites = 5
)

// matchSearchSizes are the numbers of kinks per search. Several sizes are tried, because a
// search with too few or too many kinks can return no or too many results.
var matchSearchSizes = []int{3, 1, 5}

// matchProfile is a loaded profile of a candidate.
type matchProfile struct {
	Name  string
	Kinks map[string]string
	// Fields are the values of the compared list fields, by list name (see searchFields).
	Fields  map[string]string
	Summary string
	Fetched time.Time
}

type matchState struct {
	// running makes sure only one search runs per login at a time.
	running sync.Mutex
	lock    sync.Mutex
	cache   map[networkid.UserID]*matchProfile
}

// matchCandidate is an online character that may match.
type matchCandidate struct {
	ID       networkid.UserID
	Name     string
	Gender   string
	Priority float64
}

// factorResult is how well one part of two profiles fits together, from 0 to 1.
type factorResult struct {
	Label string
	// Value is shown next to the label, for fields that aren't in the profile summary.
	Value string
	Score float64
}

// matchResult is a compared candidate.
type matchResult struct {
	Candidate *matchCandidate
	Profile   *matchProfile
	Match     *kinkMatch
	Factors   []factorResult
	Score     float64
}

// List names of the compared fields, as used by the F-List mapping list.
const (
	listGender      = "gender"
	listOrientation = "orientation"
	listLanguage    = "languagepreference"
	listFurry       = "furrypref"
	listRole        = "subdom"
	listPosition    = "position"
)

// Weights of the parts of a match. Missing fields aren't counted.
const (
	weightKinks       = 0.5
	weightOrientation = 0.2
	weightRole        = 0.1
	weightLanguage    = 0.1
	weightFurry       = 0.05
	weightPosition    = 0.05
)

// kinkScore ranks a kink comparison. It's the lower bound of the Wilson score interval of
// the share of agreeing kinks, so that a few agreeing kinks don't rank above many agreeing
// kinks with a single conflict.
func kinkScore(m *kinkMatch) float64 {
	n := float64(m.Shared + m.Conflicts)
	if n == 0 || m.Shared == 0 {
		return 0
	}
	const z = 1.96
	p := float64(m.Shared) / n
	return (p + z*z/(2*n) - z*math.Sqrt((p*(1-p)+z*z/(4*n))/n)) / (1 + z*z/n)
}

func (m *kinkMatch) percent() int {
	total := m.Shared + m.Conflicts
	if total == 0 {
		return 0
	}
	return (m.Shared*100 + total/2) / total
}

// genderClass sorts F-List genders into masculine ("m"), feminine ("f") and other ("o").
func genderClass(gender string) string {
	switch strings.ToLower(strings.TrimSpace(gender)) {
	case "male", "cunt-boy":
		return "m"
	case "female", "shemale", "herm":
		return "f"
	case "":
		return ""
	}
	return "o"
}

// attraction is how well a character with the orientation fits a partner of the gender.
// ok is false when the orientation or a gender is unknown.
func attraction(orientation, ownGender, otherGender string) (score float64, ok bool) {
	own, other := genderClass(ownGender), genderClass(otherGender)
	if other == "" {
		return 0, false
	}
	same := own == other && own != "o"
	opposite := own != other && own != "o" && other != "o"
	pick := func(sameScore, oppositeScore float64) float64 {
		switch {
		case own == "":
			return 0.5
		case same:
			return sameScore
		case opposite:
			return oppositeScore
		}
		return 0.5
	}
	switch o := strings.ToLower(strings.TrimSpace(orientation)); {
	case o == "straight":
		return pick(0, 1), true
	case o == "gay":
		return pick(1, 0), true
	case o == "bisexual", o == "pansexual", o == "unsure":
		return 1, true
	case o == "bi-curious":
		return pick(0.7, 1), true
	case strings.Contains(o, "male preference") && !strings.Contains(o, "female"):
		return map[bool]float64{true: 1, false: 0.7}[other == "m"], true
	case strings.Contains(o, "female preference"):
		return map[bool]float64{true: 1, false: 0.7}[other == "f"], true
	case o == "asexual":
		return 0.5, true
	}
	return 0, false
}

// orientationFit is how well two characters fit each other by gender and orientation.
func orientationFit(ownOrientation, ownGender, otherOrientation, otherGender string) (float64, bool) {
	mine, okMine := attraction(ownOrientation, ownGender, otherGender)
	theirs, okTheirs := attraction(otherOrientation, otherGender, ownGender)
	switch {
	case okMine && okTheirs:
		return math.Sqrt(mine * theirs), true
	case okMine:
		return mine, true
	case okTheirs:
		return theirs, true
	}
	return 0, false
}

// scaleValue places a value on a scale from -2 to 2.
func scaleValue(value string, scale map[string]float64) (float64, bool) {
	pos, ok := scale[strings.ToLower(strings.TrimSpace(value))]
	return pos, ok
}

var roleScale = map[string]float64{
	"always dominant": 2, "usually dominant": 1, "switch": 0, "usually submissive": -1, "always submissive": -2,
}

var positionScale = map[string]float64{
	"always top": 2, "usually top": 1, "switch": 0, "usually bottom": -1, "always bottom": -2,
}

// complementFit is how well two values of a dom/sub-like scale complement each other:
// dominant and submissive fit, two dominants don't.
func complementFit(own, other string, scale map[string]float64) (float64, bool) {
	a, okA := scaleValue(own, scale)
	b, okB := scaleValue(other, scale)
	if !okA || !okB {
		return 0, false
	}
	return 1 - math.Abs(a+b)/4, true
}

// furryPosition places a furry preference on a scale from 0 (only humans) to 4 (only furries).
func furryPosition(value string) (float64, bool) {
	v := strings.ToLower(value)
	switch {
	case strings.Contains(v, "no furry"):
		return 0, true
	case strings.Contains(v, "humans preferred"):
		return 1, true
	case strings.Contains(v, "and / or"), strings.Contains(v, "and/or"):
		return 2, true
	case strings.Contains(v, "furries preferred"):
		return 3, true
	case strings.Contains(v, "no humans"):
		return 4, true
	}
	return 0, false
}

func furryFit(own, other string) (float64, bool) {
	a, okA := furryPosition(own)
	b, okB := furryPosition(other)
	if !okA || !okB {
		return 0, false
	}
	return 1 - math.Abs(a-b)/4, true
}

func languageFit(own, other string) (float64, bool) {
	own, other = strings.TrimSpace(own), strings.TrimSpace(other)
	switch {
	case own == "" || other == "":
		return 0, false
	case strings.EqualFold(own, other):
		return 1, true
	case strings.EqualFold(own, "other") || strings.EqualFold(other, "other"):
		return 0.5, true
	}
	return 0, true
}

// compareProfiles scores how well a candidate's profile fits the own profile.
func compareProfiles(own, theirs *matchProfile, theirGender string, kinkNames map[string]string) *matchResult {
	result := &matchResult{Profile: theirs, Match: compareKinks(own.Kinks, theirs.Kinks, kinkNames)}
	total, weights := weightKinks*kinkScore(result.Match), weightKinks
	add := func(label, value string, weight, score float64, ok bool) {
		if !ok {
			return
		}
		result.Factors = append(result.Factors, factorResult{Label: label, Value: value, Score: score})
		total += weight * score
		weights += weight
	}
	if g := theirs.Fields[listGender]; g != "" {
		theirGender = g
	}
	score, ok := orientationFit(own.Fields[listOrientation], own.Fields[listGender], theirs.Fields[listOrientation], theirGender)
	add("gender and orientation", "", weightOrientation, score, ok)
	score, ok = complementFit(own.Fields[listRole], theirs.Fields[listRole], roleScale)
	add("role", "", weightRole, score, ok)
	score, ok = complementFit(own.Fields[listPosition], theirs.Fields[listPosition], positionScale)
	add("position", theirs.Fields[listPosition], weightPosition, score, ok)
	score, ok = languageFit(own.Fields[listLanguage], theirs.Fields[listLanguage])
	add("language", theirs.Fields[listLanguage], weightLanguage, score, ok)
	score, ok = furryFit(own.Fields[listFurry], theirs.Fields[listFurry])
	add("furry preference", theirs.Fields[listFurry], weightFurry, score, ok)
	result.Score = total / weights
	return result
}

// newMatchProfile extracts the compared parts of an F-List profile.
func newMatchProfile(data *fchat.CharacterData, name string, opts *searchOptions) *matchProfile {
	profile := &matchProfile{
		Name:    cmp.Or(data.Name, name),
		Kinks:   data.Kinks,
		Fields:  make(map[string]string),
		Fetched: time.Now(),
	}
	profile.Summary, _, _ = strings.Cut(profileSummary(data, opts.ListItems), "\n")
	for _, list := range []string{listGender, listOrientation, listLanguage, listFurry, listRole, listPosition} {
		if id := opts.InfotagByList[list]; id != "" {
			if value := opts.ListItems[strings.TrimSpace(data.Infotags[id])]; value != "" {
				profile.Fields[list] = value
			}
		}
	}
	return profile
}

// searchKinkIDs returns the IDs of the kinks used to find candidates: the favorites, or the
// liked kinks if there are no favorites.
func searchKinkIDs(kinks map[string]string) []string {
	var faves, yes []string
	for id, choice := range kinks {
		switch choice {
		case "fave":
			faves = append(faves, id)
		case "yes":
			yes = append(yes, id)
		}
	}
	if len(faves) == 0 {
		faves = yes
	}
	rand.Shuffle(len(faves), func(i, j int) { faves[i], faves[j] = faves[j], faves[i] })
	return faves
}

// searchOnline runs character searches for subsets of the kinks and returns the online
// characters found, by user ID.
func (c *FChatClient) searchOnline(ctx context.Context, kinks []string) (map[networkid.UserID]string, error) {
	found := make(map[networkid.UserID]string)
	var lastErr error
	offset, throttled := 0, 0
	for searches := 0; searches < matchSearches && len(found) < 2*maxMatchCandidates; {
		wait := matchSearchInterval
		if throttled > 0 {
			wait = retryWait(throttled)
		}
		if searches > 0 || throttled > 0 {
			select {
			case <-ctx.Done():
				return found, ctx.Err()
			case <-time.After(wait):
			}
		}
		size := min(matchSearchSizes[searches%len(matchSearchSizes)], maxSearchKinks, len(kinks))
		subset := make([]string, size)
		for j := range subset {
			subset[j] = kinks[(offset+j)%len(kinks)]
		}
		var result fchat.SearchResult
		err := c.Conn.RequestOrError(ctx, fchat.CmdSearch, &fchat.SearchRequest{Kinks: subset}, fchat.CmdSearch, &result,
			fchat.ErrCodeNoSearchResults, fchat.ErrCodeTooManyResults, fchat.ErrCodeSearchThrottled)
		var fcErr *fchat.Error
		if errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeSearchThrottled && throttled < matchRetries {
			// Try the same search again after waiting.
			throttled++
			c.log.Debug().Int("try", throttled).Msg("Character search throttled, trying again")
			lastErr = err
			continue
		}
		searches, throttled = searches+1, 0
		offset += size
		if err != nil {
			c.log.Debug().Err(err).Int("kinks", size).Msg("Character search for matching failed")
			lastErr = err
			continue
		}
		for _, name := range result.Characters {
			found[makeUserID(name)] = name
		}
	}
	if len(found) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return found, nil
}

// retryWait is the wait before the given retry (1, 2, …): 5, 10, 20, … seconds.
func retryWait(try int) time.Duration {
	return matchRetryWait << (try - 1)
}

// matchCandidates turns search results into candidates, the most promising first.
func (c *FChatClient) matchCandidates(found map[networkid.UserID]string, own *matchProfile) []*matchCandidate {
	candidates := make([]*matchCandidate, 0, len(found))
	c.stateLock.RLock()
	for userID, name := range found {
		info, ok := c.characters[userID]
		if userID == "" || userID == c.ownID || (ok && info.Status == fchat.StatusOffline) {
			continue
		}
		cand := &matchCandidate{ID: userID, Name: name}
		if ok {
			cand.Name, cand.Gender = cmp.Or(info.Name, name), info.Gender
			if info.Status == fchat.StatusLooking {
				cand.Priority += 0.5
			}
		}
		// The gender is known before loading the profile, so prefer genders the user likes.
		if score, ok := attraction(own.Fields[listOrientation], own.Fields[listGender], cand.Gender); ok {
			cand.Priority += score
		}
		candidates = append(candidates, cand)
	}
	c.stateLock.RUnlock()
	candidates = slices.DeleteFunc(candidates, func(cand *matchCandidate) bool { return c.isIgnored(cand.Name) })

	// Characters with loaded profiles cost nothing, so they're always compared.
	c.matches.lock.Lock()
	for _, cand := range candidates {
		if profile, ok := c.matches.cache[cand.ID]; ok && time.Since(profile.Fetched) < matchProfileMaxAge {
			cand.Priority += 10
		}
	}
	c.matches.lock.Unlock()
	// Shuffle first, so that searches don't always pick the same characters of equal priority.
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	slices.SortStableFunc(candidates, func(a, b *matchCandidate) int { return cmp.Compare(b.Priority, a.Priority) })
	return candidates
}

// matchProfileOf returns the profile of a candidate, from the cache or loaded from F-List.
// fetched is true when it was loaded.
func (c *FChatClient) matchProfileOf(ctx context.Context, cand *matchCandidate, opts *searchOptions) (profile *matchProfile, fetched bool, err error) {
	c.matches.lock.Lock()
	profile, ok := c.matches.cache[cand.ID]
	c.matches.lock.Unlock()
	if ok && time.Since(profile.Fetched) < matchProfileMaxAge {
		return profile, false, nil
	}
	var data *fchat.CharacterData
	for try := 0; ; try++ {
		err = c.withTicket(ctx, func(account, ticket string) error {
			var reqErr error
			data, reqErr = c.Main.API.GetCharacterData(ctx, account, ticket, cand.Name)
			return reqErr
		})
		if err == nil || !fchat.IsRateLimited(err) || try >= matchRetries {
			break
		}
		c.log.Debug().Err(err).Int("try", try+1).Str("character", cand.Name).Msg("F-List rate limit, loading profile again")
		select {
		case <-ctx.Done():
			return nil, true, ctx.Err()
		case <-time.After(retryWait(try + 1)):
		}
	}
	if err != nil {
		return nil, true, err
	}
	profile = newMatchProfile(data, cand.Name, opts)
	c.matches.lock.Lock()
	if c.matches.cache == nil {
		c.matches.cache = make(map[networkid.UserID]*matchProfile)
	}
	for userID, old := range c.matches.cache {
		if time.Since(old.Fetched) >= matchProfileMaxAge {
			delete(c.matches.cache, userID)
		}
	}
	c.matches.cache[cand.ID] = profile
	c.matches.lock.Unlock()
	return profile, true, nil
}

func (fc *FChatConnector) cmdFindMatches(ce *commands.Event) {
	client := fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if !client.matches.running.TryLock() {
		ce.Reply("A search for matches is already running, please wait for it to finish")
		return
	}
	defer client.matches.running.Unlock()
	ctx, cancel := context.WithTimeout(ce.Ctx, matchTimeout)
	defer cancel()
	character := client.meta().Character

	opts, err := fc.getSearchOptions(ctx)
	if err != nil {
		ce.Reply("Failed to load the kink list from F-List: %v", err)
		return
	}
	ownData, err := client.ownCharacterData(ctx)
	if err != nil {
		replyAPIError(ce, "load the profile of "+character, err)
		return
	}
	own := newMatchProfile(ownData, character, opts)
	kinks := searchKinkIDs(own.Kinks)
	if len(kinks) == 0 {
		ce.Reply("The profile of **%s** has no favorite or liked kinks. They're needed to search for online characters.", character)
		return
	}
	ce.Reply("⏳ Searching online characters and comparing their profiles with **%s**. "+
		"**This usually takes about a minute, sometimes up to 3 minutes:** F-Chat only allows a character search "+
		"every few seconds, and the profiles are loaded one after another. If F-Chat or F-List ask the bridge to slow "+
		"down, it waits and tries again. The results are posted here when they're ready.", character)

	searchCtx, cancelSearch := context.WithTimeout(ctx, matchSearchTimeout)
	found, err := client.searchOnline(searchCtx, kinks)
	cancelSearch()
	if err != nil && len(found) == 0 {
		var fcErr *fchat.Error
		switch {
		case errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeNoSearchResults:
			ce.Reply("No online characters share the favorite kinks of **%s** right now", character)
		case errors.As(err, &fcErr) && fcErr.Code == fchat.ErrCodeSearchThrottled, errors.Is(err, context.DeadlineExceeded):
			ce.Reply("F-Chat kept refusing the character search because of its search limit. Please try again in a few minutes.")
		default:
			ce.Reply("The character search failed: %v", err)
		}
		return
	}
	candidates := client.matchCandidates(found, own)
	if len(candidates) == 0 {
		ce.Reply("No online characters share the favorite kinks of **%s** right now", character)
		return
	}
	considered := len(candidates)
	candidates = candidates[:min(len(candidates), maxMatchCandidates)]

	var results []*matchResult
	compared := 0
	for _, cand := range candidates {
		profile, fetched, err := client.matchProfileOf(ctx, cand, opts)
		if ctx.Err() != nil {
			break
		} else if err != nil {
			ce.Log.Debug().Err(err).Str("character", cand.Name).Msg("Failed to load profile for matching")
		} else {
			compared++
			result := compareProfiles(own, profile, cand.Gender, opts.KinkNames)
			result.Candidate = cand
			results = append(results, result)
		}
		if fetched {
			select {
			case <-ctx.Done():
			case <-time.After(matchFetchInterval):
			}
		}
	}
	if len(results) == 0 {
		ce.Reply("Couldn't load the profiles of the online characters that were found (F-List may be limiting requests), " +
			"please try again in a few minutes")
		return
	}
	slices.SortFunc(results, func(a, b *matchResult) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.Match.Shared, a.Match.Shared))
	})
	results = results[:min(len(results), matchResults)]

	entries := make([]*contactEntry, len(results))
	for i, result := range results {
		entries[i] = client.contactEntries(map[networkid.UserID]string{result.Candidate.ID: result.Profile.Name}, "")[0]
	}
	client.loadAvatars(ctx, entries)
	content := fc.renderMatches(character, results, entries, compared, considered)
	if _, err = ce.Bot.SendMessage(ce.Ctx, ce.OrigRoomID, event.EventMessage, &event.Content{Parsed: content}, nil); err != nil {
		ce.Log.Err(err).Msg("Failed to send matches")
	}
}

func factorIcon(score float64) string {
	switch {
	case score >= 0.75:
		return "✅"
	case score >= 0.4:
		return "➖"
	}
	return "❌"
}

// describeFactors is a line like "✅ gender and orientation · ➖ role · ✅ language (English)".
func describeFactors(factors []factorResult) string {
	parts := make([]string, len(factors))
	for i, f := range factors {
		parts[i] = factorIcon(f.Score) + " " + f.Label
		if f.Value != "" {
			parts[i] += " (" + f.Value + ")"
		}
	}
	return strings.Join(parts, " · ")
}

// renderMatches renders the best matches with their avatars, match details and profile summaries.
func (fc *FChatConnector) renderMatches(own string, results []*matchResult, entries []*contactEntry, compared, considered int) *event.MessageEventContent {
	header := fmt.Sprintf("Best matches for %s (compared %d online characters", own, compared)
	if considered > compared {
		header += fmt.Sprintf(" of %d found", considered)
	}
	header += ")"
	var plain, htmlOut strings.Builder
	plain.WriteString(header + ":\n\n")
	fmt.Fprintf(&htmlOut, "<p>%s:</p>\n<ol>\n", html.EscapeString(header))
	for i, result := range results {
		entry, m := entries[i], result.Match
		score := fmt.Sprintf("%d%% match", int(math.Round(result.Score*100)))
		kinks := fmt.Sprintf("Kinks: %d%% (%d liked by both, %d conflicts)", m.percent(), m.Shared, m.Conflicts)
		lines := []string{kinks}
		if factors := describeFactors(result.Factors); factors != "" {
			lines = append(lines, factors)
		}
		if result.Profile.Summary != "" {
			lines = append(lines, result.Profile.Summary)
		}
		if len(m.BothFavorites) > 0 {
			lines = append(lines, "Favorites of both: "+limitList(m.BothFavorites, maxMatchFavorites))
		}
		status := entry.statusText()

		fmt.Fprintf(&plain, "%d. %s – %s – %s\n", i+1, entry.Name, score, status)
		for _, line := range lines {
			fmt.Fprintf(&plain, "   %s\n", line)
		}

		htmlOut.WriteString("<li><p>")
		if entry.AvatarMXC != "" {
			fmt.Fprintf(&htmlOut, `<img src="%s" alt="" title="%s" width="%d" height="%d"> `,
				html.EscapeString(entry.AvatarMXC), html.EscapeString(entry.Name), avatarSize, avatarSize)
		}
		profile := fchat.ProfileURL(fc.Config.SiteURL, entry.Name)
		fmt.Fprintf(&htmlOut, "%s – <strong>%s</strong> – %s",
			renderName(entry.Name, entry.Gender, profile, entry.ChatLink), html.EscapeString(score), html.EscapeString(status))
		for _, line := range lines {
			fmt.Fprintf(&htmlOut, "<br>\n%s", html.EscapeString(line))
		}
		htmlOut.WriteString("</p></li>\n")
	}
	htmlOut.WriteString("</ol>\n")
	note := "Compared: standard kinks, gender and orientation, dom/sub role, position, language and furry preference."
	startChat := fc.Bridge.Config.CommandPrefix + " start-chat <name>"
	plain.WriteString("\n" + note + " Start a private chat with `" + startChat + "`")
	fmt.Fprintf(&htmlOut, "<p>%s Click a name to open the F-List profile, 💬 opens the private chat. "+
		"You can also use <code>%s</code>.</p>", html.EscapeString(note), html.EscapeString(startChat))
	content := &event.MessageEventContent{
		MsgType:       event.MsgNotice,
		Body:          plain.String(),
		Format:        event.FormatHTML,
		FormattedBody: htmlOut.String(),
		Mentions:      &event.Mentions{},
	}
	disableLinkPreviews(content)
	return content
}
