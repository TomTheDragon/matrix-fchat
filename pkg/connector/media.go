package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/fchat"
)

// Matrix clients only show mxc:// images inline, so eicons and emoticons are
// uploaded once and the mxc:// URIs are kept in the bridge's key-value store.

const (
	// The key-value store keeps JSON-encoded cachedImages. Older versions stored only the mxc:// URI.
	imageCacheKeyPrefix = "fchat_image:"
	// imageFailureTTL is how long a failed download isn't retried (e.g. eicons that don't exist).
	imageFailureTTL = time.Hour
	imageTimeout    = 15 * time.Second
)

// cachedImage is an F-List image that was uploaded to Matrix.
type cachedImage struct {
	MXC      id.ContentURIString `json:"mxc"`
	MimeType string              `json:"mimetype"`
	Width    int                 `json:"w,omitempty"`
	Height   int                 `json:"h,omitempty"`
	Size     int                 `json:"size,omitempty"`
	Animated bool                `json:"animated,omitempty"`
}

type imageCache struct {
	lock   sync.Mutex
	known  map[string]*cachedImage
	failed map[string]time.Time
}

func (ic *imageCache) get(url string) (img *cachedImage, failed bool) {
	ic.lock.Lock()
	defer ic.lock.Unlock()
	if failedAt, ok := ic.failed[url]; ok && time.Since(failedAt) < imageFailureTTL {
		return nil, true
	}
	return ic.known[url], false
}

func (ic *imageCache) set(url string, img *cachedImage) {
	ic.lock.Lock()
	defer ic.lock.Unlock()
	if img == nil {
		if ic.failed == nil {
			ic.failed = make(map[string]time.Time)
		}
		ic.failed[url] = time.Now()
		return
	}
	if ic.known == nil {
		ic.known = make(map[string]*cachedImage)
	}
	ic.known[url] = img
	delete(ic.failed, url)
}

// describeImage reads the type, size and animation of an image.
func describeImage(data []byte) *cachedImage {
	img := &cachedImage{MimeType: http.DetectContentType(data), Size: len(data)}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		img.Width, img.Height = cfg.Width, cfg.Height
	}
	if img.MimeType == "image/gif" {
		if anim, err := gif.DecodeAll(bytes.NewReader(data)); err == nil && len(anim.Image) > 1 {
			img.Animated = true
		}
	}
	return img
}

// imageMXC returns the mxc:// URI of an image from F-List, uploading it if necessary.
// It returns "" if the image can't be downloaded.
func (fc *FChatConnector) imageMXC(ctx context.Context, url string) id.ContentURIString {
	if img := fc.image(ctx, url); img != nil {
		return img.MXC
	}
	return ""
}

// image returns an image from F-List uploaded to Matrix, uploading it if necessary.
// It returns nil if the image can't be downloaded.
func (fc *FChatConnector) image(ctx context.Context, url string) *cachedImage {
	if img, failed := fc.images.get(url); img != nil || failed {
		return img
	}
	kvKey := database.Key(imageCacheKeyPrefix + url)
	if stored := fc.Bridge.DB.KV.Get(ctx, kvKey); strings.HasPrefix(stored, "{") {
		var img cachedImage
		if err := json.Unmarshal([]byte(stored), &img); err == nil && img.MXC != "" {
			fc.images.set(url, &img)
			return &img
		}
	}
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	log := fc.Bridge.Log.With().Str("image_url", url).Logger()
	data, err := fc.download(ctx, url)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to download image")
		fc.images.set(url, nil)
		return nil
	}
	img := describeImage(data)
	if !strings.HasPrefix(img.MimeType, "image/") {
		log.Debug().Str("mime_type", img.MimeType).Msg("Downloaded file isn't an image")
		fc.images.set(url, nil)
		return nil
	}
	img.MXC, _, err = fc.Bridge.Bot.UploadMedia(ctx, "", data, path.Base(url), img.MimeType)
	if err != nil {
		// Don't remember upload failures, they're probably temporary.
		log.Warn().Err(err).Msg("Failed to upload image")
		return nil
	}
	if encoded, err := json.Marshal(img); err == nil {
		fc.Bridge.DB.KV.Set(ctx, kvKey, string(encoded))
	}
	fc.images.set(url, img)
	return img
}

// combinedEIcons returns one image with several eicons next to each other, uploaded to Matrix.
// Combinations are cached like single images. It returns nil if an eicon can't be loaded.
func (fc *FChatConnector) combinedEIcons(ctx context.Context, names []string) *cachedImage {
	if len(names) > maxCombinedEIcons {
		return nil
	}
	lower := make([]string, len(names))
	for i, name := range names {
		lower[i] = strings.ToLower(name)
	}
	key := "eicons:" + strings.Join(lower, "|")
	if img, failed := fc.images.get(key); img != nil || failed {
		return img
	}
	kvKey := database.Key(imageCacheKeyPrefix + key)
	if stored := fc.Bridge.DB.KV.Get(ctx, kvKey); stored != "" {
		var img cachedImage
		if err := json.Unmarshal([]byte(stored), &img); err == nil && img.MXC != "" {
			fc.images.set(key, &img)
			return &img
		}
	}
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	log := fc.Bridge.Log.With().Strs("eicons", names).Logger()
	sources := make([][]animFrame, len(names))
	decoded := make(map[string][]animFrame)
	for i, name := range lower {
		if frames, ok := decoded[name]; ok {
			sources[i] = frames
			continue
		}
		data, err := fc.download(ctx, fchat.EIconURL(fc.Config.StaticURL, name))
		if err == nil {
			sources[i], err = decodeFrames(data, eiconCell)
		}
		if err != nil {
			log.Debug().Err(err).Str("eicon", name).Msg("Failed to load eicon for combined image")
			fc.images.set(key, nil)
			return nil
		}
		decoded[name] = sources[i]
	}
	data, mimeType, width, height, animated, err := combineEIcons(sources, eiconCell)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to combine eicons")
		fc.images.set(key, nil)
		return nil
	}
	img := &cachedImage{MimeType: mimeType, Width: width, Height: height, Size: len(data), Animated: animated}
	fileName := "eicons.png"
	if animated {
		fileName = "eicons.gif"
	}
	img.MXC, _, err = fc.Bridge.Bot.UploadMedia(ctx, "", data, fileName, mimeType)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to upload combined eicons")
		return nil
	}
	if encoded, err := json.Marshal(img); err == nil {
		fc.Bridge.DB.KV.Set(ctx, kvKey, string(encoded))
	}
	fc.images.set(key, img)
	return img
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
