package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"matrix-fchat/pkg/bbcode"
	"matrix-fchat/pkg/fchat"
)

// Ghosts get custom Matrix profile fields (Matrix v1.16 / MSC4133): a biography with a short
// summary of the F-List profile and a link to it, pronouns based on the gender,
// and a name color based on the gender. There are no stable keys yet, so the keys of the
// MSCs and of the clients that show these fields are set.
var (
	pronounKeys    = []string{"m.pronouns", "io.fsky.nyx.pronouns"} // MSC4247
	biographyKeys  = []string{"m.biography", "gay.fomx.biography"}  // MSC4440
	htmlBioKey     = "moe.sable.app.bio"                            // Sable: HTML string
	commetBioKey   = "chat.commet.profile_bio"                      // Commet and Sable
	nameColorKey   = "moe.sable.app.name_color"                     // Sable: #rrggbb
	allProfileKeys = append(append(append([]string{}, pronounKeys...), biographyKeys...), htmlBioKey, commetBioKey, nameColorKey)

	// beeperProfileKeys are the fields that mautrix adds to every ghost profile. They're only
	// set if beeper_profile_fields is enabled.
	beeperProfileKeys = []string{
		"com.beeper.bridge.remote_id", "com.beeper.bridge.identifiers", "com.beeper.bridge.service",
		"com.beeper.bridge.network", "com.beeper.bridge.is_bridge_bot", "com.beeper.bridge.is_network_bot",
	}
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

// biography returns a character's Matrix biography as plain text and HTML: the short summary
// of the F-List profile (if it was loaded) and a link to the profile.
func (c *FChatClient) biography(name string, meta *GhostMetadata) (plain, htmlBio string) {
	var plainLines, htmlLines []string
	if meta.ProfileSummary != "" {
		for _, line := range strings.Split(meta.ProfileSummary, "\n") {
			plainLines = append(plainLines, line)
			htmlLines = append(htmlLines, html.EscapeString(line))
		}
	}
	if name != "" && c.Main != nil {
		profile := fchat.ProfileURL(c.Main.Config.SiteURL, name)
		plainLines = append(plainLines, "F-List profile: "+profile)
		htmlLines = append(htmlLines, fmt.Sprintf(`<a href="%s">F-List profile</a>`, html.EscapeString(profile)))
	}
	return strings.Join(plainLines, "\n"), strings.Join(htmlLines, "<br>")
}

// profileFields returns the custom profile fields of a ghost. Fields without a value are
// set to null, which removes them.
func (c *FChatClient) profileFields(name string, meta *GhostMetadata) database.ExtraProfile {
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
	if plain, htmlBio := c.biography(name, meta); plain != "" {
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
	// The name was set by makeUserInfo, with the offline suffix if the ghost is offline.
	// The info may come without a name, then the old suffix stays.
	if suffix := c.Main.offlineSuffix(ghost.ID); meta.NameSuffix != suffix && strings.HasSuffix(ghost.Name, suffix) &&
		(meta.NameSuffix == "" || !strings.HasSuffix(ghost.Name, meta.NameSuffix)) {
		meta.NameSuffix = suffix
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
	if c.Main.setProfileFields(ctx, ghost, c.profileFields(name, meta)) {
		changed = true
	}
	return changed
}

// setProfileFields sets the custom profile fields of a ghost and returns whether the ghost
// changed. Without beeper_profile_fields, the fields are set directly, because mautrix always
// adds its com.beeper.bridge.* fields; those are removed instead.
func (fc *FChatConnector) setProfileFields(ctx context.Context, ghost *bridgev2.Ghost, fields database.ExtraProfile) bool {
	if fc.Config.BeeperProfileFields {
		return ghost.UpdateContactInfo(ctx, nil, nil, fields)
	}
	meta, ok := ghost.Metadata.(*GhostMetadata)
	mc := fc.matrixConnector()
	if !ok || mc == nil {
		return false
	}
	for _, key := range beeperProfileKeys {
		_ = fields.Set(key, nil)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return false
	}
	hash := sha256.Sum256(encoded)
	fieldsHash := hex.EncodeToString(hash[:])
	if meta.ProfileFieldsHash == fieldsHash {
		return false
	}
	log := zerolog.Ctx(ctx).With().Str("ghost_id", string(ghost.ID)).Logger()
	intent := mc.AS.Intent(ghost.Intent.GetMXID())
	current, err := intent.GetProfile(ctx, intent.UserID)
	if err != nil {
		log.Err(err).Msg("Failed to get ghost profile")
		return false
	}
	for key, value := range fields {
		existing, exists := current.Extra[key]
		isNull := bytes.Equal(value, []byte("null"))
		switch {
		case !exists && isNull:
			continue
		case exists && isNull:
			err = intent.DeleteProfileField(ctx, key)
		default:
			if exists {
				if existingJSON, _ := json.Marshal(existing); bytes.Equal(existingJSON, value) {
					continue
				}
			}
			err = intent.SetProfileField(ctx, key, value)
		}
		if err != nil {
			log.Err(err).Str("field", key).Msg("Failed to update ghost profile field")
			return false
		}
	}
	meta.ProfileFieldsHash = fieldsHash
	return true
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
