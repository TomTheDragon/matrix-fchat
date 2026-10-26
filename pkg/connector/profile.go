package connector

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

// Ghosts get custom Matrix profile fields (Matrix v1.16 / MSC4133): a biography with the
// F-Chat status and a short summary of the F-List profile, pronouns based on the gender,
// and a name color based on the gender. There are no stable keys yet, so the keys of the
// MSCs and of the clients that show these fields are set.
var (
	pronounKeys    = []string{"m.pronouns", "io.fsky.nyx.pronouns"} // MSC4247
	biographyKeys  = []string{"m.biography", "gay.fomx.biography"}  // MSC4440
	htmlBioKey     = "moe.sable.app.bio"                            // Sable: HTML string
	commetBioKey   = "chat.commet.profile_bio"                      // Commet and Sable
	nameColorKey   = "moe.sable.app.name_color"                     // Sable: #rrggbb
	allProfileKeys = append(append(append([]string{}, pronounKeys...), biographyKeys...), htmlBioKey, commetBioKey, nameColorKey)
)

const (
	// lastSeenInterval is how often LastSeen is updated in the database.
	lastSeenInterval = 12 * time.Hour
	// profileMaxAge is how often F-List profiles are loaded again.
	profileMaxAge = 7 * 24 * time.Hour
	// profileFetchInterval is the minimum time between two profile requests to F-List.
	profileFetchInterval = 3 * time.Second
	maxBioStatusLength   = 300
	maxLookingForLength  = 300
)

// pronounsFor returns the pronouns for an F-List gender: he/him for Male, she/her for Female,
// they/them for every other gender and "" if the gender is unknown.
func pronounsFor(gender string) string {
	switch strings.ToLower(strings.TrimSpace(gender)) {
	case "":
		return ""
	case "male":
		return "he/him"
	case "female":
		return "she/her"
	default:
		return "they/them"
	}
}

func truncateRunes(s string, n int) string {
	if runes := []rune(s); len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return s
}

// biography returns the lines of a character's Matrix biography.
func (c *FChatClient) biography(userID networkid.UserID, meta *GhostMetadata) []string {
	var lines []string
	if meta.ProfileSummary != "" {
		lines = append(lines, strings.Split(meta.ProfileSummary, "\n")...)
	}
	c.stateLock.RLock()
	info, online := c.characters[userID]
	var status fchat.Status
	var statusMsg string
	if online {
		status, statusMsg = info.Status, info.StatusMsg
	}
	c.stateLock.RUnlock()
	if online && status != "" && status != fchat.StatusOffline {
		line := "Status: " + string(status)
		if msg := strings.TrimSpace(bbcode.ToPlain(bbcode.DecodeEntities(statusMsg))); msg != "" {
			line += " – " + truncateRunes(msg, maxBioStatusLength)
		}
		lines = append(lines, line)
	}
	return lines
}

// profileFields returns the custom profile fields of a ghost. Fields without a value are
// set to null, which removes them.
func (c *FChatClient) profileFields(userID networkid.UserID, meta *GhostMetadata) database.ExtraProfile {
	fields := make(database.ExtraProfile)
	set := func(keys []string, value any) {
		for _, key := range keys {
			_ = fields.Set(key, value)
		}
	}
	if pronouns := pronounsFor(meta.Gender); pronouns != "" {
		set(pronounKeys, []map[string]string{{"language": "en", "summary": pronouns}})
	} else {
		set(pronounKeys, nil)
	}
	if color := genderColor(meta.Gender); color != "" {
		set([]string{nameColorKey}, color)
	} else {
		set([]string{nameColorKey}, nil)
	}
	if lines := c.biography(userID, meta); len(lines) > 0 {
		plain := strings.Join(lines, "\n")
		escaped := make([]string, len(lines))
		for i, line := range lines {
			escaped[i] = html.EscapeString(line)
		}
		htmlBio := strings.Join(escaped, "<br>")
		set(biographyKeys, map[string]any{"m.text": []map[string]string{
			{"body": htmlBio, "mimetype": "text/html"},
			{"body": plain},
		}})
		set([]string{htmlBioKey}, htmlBio)
		set([]string{commetBioKey}, map[string]string{"format": "org.matrix.custom.html", "formatted_body": htmlBio})
	} else {
		set(append(append([]string{}, biographyKeys...), htmlBioKey, commetBioKey), nil)
	}
	return fields
}

// extraProfileSupported returns whether the homeserver accepts custom profile fields from the bridge.
func (fc *FChatConnector) extraProfileSupported() bool {
	return fc.Bridge != nil && fc.Bridge.Matrix != nil && fc.Bridge.Matrix.GetCapabilities().ExtraProfileMeta
}

// userInfo returns the info of a character's ghost. Besides name and avatar, it updates the
// ghost metadata (gender, last seen) and the custom profile fields when the ghost is updated.
func (c *FChatClient) userInfo(name string) *bridgev2.UserInfo {
	info := c.Main.makeUserInfo(name)
	info.ExtraUpdates = func(ctx context.Context, ghost *bridgev2.Ghost) bool {
		return c.updateGhostExtras(ctx, ghost, name)
	}
	return info
}

func (c *FChatClient) updateGhostExtras(ctx context.Context, ghost *bridgev2.Ghost, name string) bool {
	meta, ok := ghost.Metadata.(*GhostMetadata)
	if !ok {
		return false
	}
	changed := false
	now := time.Now()
	if meta.Removed {
		meta.Removed = false
		changed = true
	}
	if now.Sub(time.Unix(meta.LastSeen, 0)) > lastSeenInterval {
		meta.LastSeen = now.Unix()
		changed = true
	}
	if gender := c.onlineGender(ghost.ID); gender != "" && gender != meta.Gender {
		meta.Gender = gender
		changed = true
	}
	if !c.Main.extraProfileSupported() {
		return changed
	}
	if c.Main.Config.FetchCharacterProfiles && now.Sub(time.Unix(meta.ProfileFetched, 0)) > profileMaxAge && isValidCharacterName(name) {
		c.Main.profiles.queue(c, name)
	}
	if ghost.UpdateContactInfo(ctx, nil, nil, c.profileFields(ghost.ID, meta)) {
		changed = true
	}
	return changed
}

// profileFetcher loads F-List profiles one at a time in the background.
type profileFetcher struct {
	lock    sync.Mutex
	pending map[networkid.UserID]bool
	jobs    chan profileJob
	start   sync.Once
}

type profileJob struct {
	client *FChatClient
	name   string
}

const maxQueuedProfiles = 500

func (pf *profileFetcher) queue(c *FChatClient, name string) {
	pf.start.Do(func() {
		pf.pending = make(map[networkid.UserID]bool)
		pf.jobs = make(chan profileJob, maxQueuedProfiles)
		go pf.run()
	})
	userID := makeUserID(name)
	pf.lock.Lock()
	defer pf.lock.Unlock()
	if pf.pending[userID] {
		return
	}
	select {
	case pf.jobs <- profileJob{client: c, name: name}:
		pf.pending[userID] = true
	default:
		// The queue is full. The profile is queued again the next time the ghost is updated.
	}
}

func (pf *profileFetcher) run() {
	for job := range pf.jobs {
		job.client.fetchProfile(job.name)
		pf.lock.Lock()
		delete(pf.pending, makeUserID(job.name))
		pf.lock.Unlock()
		time.Sleep(profileFetchInterval)
	}
}

// infotag IDs from F-List's mapping list.
const (
	infotagAge         = "1"
	infotagOrientation = "2"
	infotagGender      = "3"
	infotagSpecies     = "9"
	infotagRole        = "15"
	infotagLookingFor  = "63"
)

// profileSummary builds the short profile summary, e.g. "Female · Wolf · Age 25 · Bisexual · Switch".
func profileSummary(data *fchat.CharacterData, listItems map[string]string) string {
	value := func(id string, isList bool) string {
		v := strings.TrimSpace(data.Infotags[id])
		if isList {
			v = listItems[v]
		}
		return strings.TrimSpace(bbcode.ToPlain(html.UnescapeString(v)))
	}
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	add(value(infotagGender, true))
	add(value(infotagSpecies, false))
	if age := value(infotagAge, false); age != "" {
		add("Age " + age)
	}
	add(value(infotagOrientation, true))
	add(value(infotagRole, true))
	summary := strings.Join(parts, " · ")
	if looking := value(infotagLookingFor, false); looking != "" {
		if summary != "" {
			summary += "\n"
		}
		summary += "Looking for: " + truncateRunes(strings.Join(strings.Fields(looking), " "), maxLookingForLength)
	}
	return summary
}

// fetchProfile loads a character's F-List profile and updates the ghost.
func (c *FChatClient) fetchProfile(name string) {
	ctx, cancel := context.WithTimeout(c.ctx(), time.Minute)
	defer cancel()
	log := c.log.With().Str("action", "fetch profile").Str("character", name).Logger()
	ghost, err := c.Main.Bridge.GetExistingGhostByID(ctx, makeUserID(name))
	if err != nil || ghost == nil {
		return
	}
	meta, ok := ghost.Metadata.(*GhostMetadata)
	if !ok || time.Since(time.Unix(meta.ProfileFetched, 0)) < profileMaxAge {
		return
	}
	opts, err := c.Main.getSearchOptions(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load the F-List mapping list")
		return
	}
	var data *fchat.CharacterData
	err = c.withTicket(ctx, func(account, ticket string) error {
		var reqErr error
		data, reqErr = c.Main.API.GetCharacterData(ctx, account, ticket, name)
		return reqErr
	})
	var apiErr *fchat.APIError
	switch {
	case err == nil:
		meta.ProfileSummary = profileSummary(data, opts.ListItems)
		if gender := opts.ListItems[strings.TrimSpace(data.Infotags[infotagGender])]; gender != "" && meta.Gender == "" {
			meta.Gender = gender
		}
	case errors.As(err, &apiErr):
		// E.g. the character doesn't exist anymore. Don't try again until the next refresh.
		log.Debug().Err(err).Msg("F-List didn't return the profile")
	default:
		log.Warn().Err(err).Msg("Failed to load profile")
		return
	}
	meta.ProfileFetched = time.Now().Unix()
	// Applies the new biography and saves the ghost.
	ghost.UpdateInfo(ctx, c.userInfo(name))
	if err = c.Main.Bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
		log.Err(err).Msg("Failed to save ghost after loading profile")
	}
}

// withTicket calls fn with an API ticket of the login's account, getting a new ticket once if
// the current one is rejected.
func (c *FChatClient) withTicket(ctx context.Context, fn func(account, ticket string) error) error {
	c.metaLock.Lock()
	account, password := c.meta().Account, c.meta().Password
	c.metaLock.Unlock()
	if account == "" || password == "" {
		return fmt.Errorf("login data is missing")
	}
	ticket, err := c.Main.Tickets.Get(ctx, account, password, false)
	if err != nil {
		return fmt.Errorf("failed to get API ticket: %w", err)
	}
	err = fn(account, ticket.Ticket)
	if fchat.IsInvalidTicket(err) {
		c.Main.Tickets.Invalidate(account, ticket.Ticket)
		if ticket, err = c.Main.Tickets.Get(ctx, account, password, true); err != nil {
			return fmt.Errorf("failed to get API ticket: %w", err)
		}
		err = fn(account, ticket.Ticket)
	}
	return err
}

// isKnownCharacter returns whether a character is a friend, a bookmark or in one of the
// channels, i.e. probably has a ghost that the user sees.
func (c *FChatClient) isKnownCharacter(userID networkid.UserID) bool {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	if _, ok := c.friends[userID]; ok {
		return true
	}
	if _, ok := c.bookmarks[userID]; ok {
		return true
	}
	for _, ch := range c.channels {
		if _, ok := ch.Members[userID]; ok {
			return true
		}
	}
	return false
}

// refreshGhostProfile updates the profile of an existing ghost, e.g. after a status change.
func (c *FChatClient) refreshGhostProfile(name string) {
	ctx, cancel := context.WithTimeout(c.ctx(), time.Minute)
	defer cancel()
	ghost, err := c.Main.Bridge.GetExistingGhostByID(ctx, makeUserID(name))
	if err != nil || ghost == nil {
		return
	}
	ghost.UpdateInfo(ctx, c.userInfo(name))
}
