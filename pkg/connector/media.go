package connector

import (
	"context"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"
)

// Matrix clients only show mxc:// images inline, so eicons and emoticons are
// uploaded once and the mxc:// URIs are kept in the bridge's key-value store.

const (
	imageCacheKeyPrefix = "fchat_image:"
	// imageFailureTTL is how long a failed download isn't retried (e.g. eicons that don't exist).
	imageFailureTTL = time.Hour
	imageTimeout    = 15 * time.Second
)

type imageCache struct {
	lock   sync.Mutex
	known  map[string]id.ContentURIString
	failed map[string]time.Time
}

func (ic *imageCache) get(url string) (mxc id.ContentURIString, failed bool) {
	ic.lock.Lock()
	defer ic.lock.Unlock()
	if failedAt, ok := ic.failed[url]; ok && time.Since(failedAt) < imageFailureTTL {
		return "", true
	}
	return ic.known[url], false
}

func (ic *imageCache) set(url string, mxc id.ContentURIString) {
	ic.lock.Lock()
	defer ic.lock.Unlock()
	if mxc == "" {
		if ic.failed == nil {
			ic.failed = make(map[string]time.Time)
		}
		ic.failed[url] = time.Now()
		return
	}
	if ic.known == nil {
		ic.known = make(map[string]id.ContentURIString)
	}
	ic.known[url] = mxc
	delete(ic.failed, url)
}

// imageMXC returns the mxc:// URI of an image from F-List, uploading it if necessary.
// It returns "" if the image can't be downloaded.
func (fc *FChatConnector) imageMXC(ctx context.Context, url string) id.ContentURIString {
	if mxc, failed := fc.images.get(url); mxc != "" || failed {
		return mxc
	}
	kvKey := database.Key(imageCacheKeyPrefix + url)
	if stored := fc.Bridge.DB.KV.Get(ctx, kvKey); stored != "" {
		mxc := id.ContentURIString(stored)
		fc.images.set(url, mxc)
		return mxc
	}
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	log := fc.Bridge.Log.With().Str("image_url", url).Logger()
	data, err := fc.download(ctx, url)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to download image")
		fc.images.set(url, "")
		return ""
	}
	mimeType := http.DetectContentType(data)
	if !strings.HasPrefix(mimeType, "image/") {
		log.Debug().Str("mime_type", mimeType).Msg("Downloaded file isn't an image")
		fc.images.set(url, "")
		return ""
	}
	mxc, _, err := fc.Bridge.Bot.UploadMedia(ctx, "", data, path.Base(url), mimeType)
	if err != nil {
		// Don't remember upload failures, they're probably temporary.
		log.Warn().Err(err).Msg("Failed to upload image")
		return ""
	}
	fc.Bridge.DB.KV.Set(ctx, kvKey, string(mxc))
	fc.images.set(url, mxc)
	return mxc
}

// avatarMXC returns the mxc:// URI of a character's avatar, updating the ghost if needed.
func (c *FChatClient) avatarMXC(ctx context.Context, name string) id.ContentURIString {
	userID := makeUserID(name)
	if userID == "" {
		return ""
	}
	ghost, err := c.Main.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		c.log.Err(err).Str("character", name).Msg("Failed to get ghost for avatar")
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	// Doesn't download anything if the avatar is already up to date.
	ghost.UpdateInfo(ctx, c.userInfoByID(userID))
	return ghost.AvatarMXC
}
