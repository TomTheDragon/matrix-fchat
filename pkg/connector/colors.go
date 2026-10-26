package connector

import (
	"context"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// genderColors are the name colors of the official F-Chat client (scss/_chat.scss in chat3client).
var genderColors = map[string]string{
	"shemale":     "#cc66ff",
	"herm":        "#9b30ff",
	"none":        "#adb5bd",
	"female":      "#ff6699",
	"male":        "#6699ff",
	"male-herm":   "#007fff",
	"transgender": "#ee8822",
	"cunt-boy":    "#00cc66",
}

// genderColor returns the F-Chat name color of a gender, or "" if it's unknown.
func genderColor(gender string) string {
	return genderColors[strings.ToLower(strings.TrimSpace(gender))]
}

// onlineGender returns the gender of a character that is online.
func (c *FChatClient) onlineGender(userID networkid.UserID) string {
	c.stateLock.RLock()
	defer c.stateLock.RUnlock()
	if info, ok := c.characters[userID]; ok {
		return info.Gender
	}
	return ""
}

// characterGender returns the gender of a character: from the online list, or the
// gender that was stored in the ghost's metadata the last time it was seen.
func (c *FChatClient) characterGender(ctx context.Context, userID networkid.UserID) string {
	if gender := c.onlineGender(userID); gender != "" {
		return gender
	}
	ghost, err := c.Main.Bridge.GetExistingGhostByID(ctx, userID)
	if err != nil || ghost == nil {
		return ""
	}
	if meta, ok := ghost.Metadata.(*GhostMetadata); ok {
		return meta.Gender
	}
	return ""
}
