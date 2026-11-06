package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/mediaproxy"
)

// With direct media (direct_media in the bridge config), images aren't uploaded to the
// homeserver. The bridge creates mxc:// URIs on its own media server instead, and the
// homeserver downloads the images from the bridge when someone opens them.
//
// A media ID is a kind byte followed by the data needed to get the image again.
const (
	// mediaKindURL is an image on the F-List static server. The URL may have a #fragment,
	// which is only used to get a new mxc:// URI (avatars are refreshed that way).
	mediaKindURL = 'u'
	// mediaKindEIcons is several eicons combined into one image, the lowercase names
	// separated by "|".
	mediaKindEIcons = 'e'
)

var _ bridgev2.DirectMediableNetwork = (*FChatConnector)(nil)

func (fc *FChatConnector) SetUseDirectMedia() {
	fc.directMedia = true
}

// directMXC returns a direct media mxc:// URI, or "" if direct media is off or the URI
// would be too long. Callers then upload the image instead.
func (fc *FChatConnector) directMXC(ctx context.Context, mediaID networkid.MediaID) id.ContentURIString {
	if !fc.directMedia {
		return ""
	}
	mxc, err := fc.Bridge.Matrix.GenerateContentURI(ctx, mediaID)
	if err != nil {
		fc.Bridge.Log.Debug().Err(err).Msg("Failed to generate direct media URI, uploading instead")
		return ""
	}
	return mxc
}

func urlMediaID(url string) networkid.MediaID {
	return append(networkid.MediaID{mediaKindURL}, url...)
}

func eiconsMediaID(lowerNames []string) networkid.MediaID {
	return append(networkid.MediaID{mediaKindEIcons}, strings.Join(lowerNames, "|")...)
}

// directAvatar returns an avatar that points at the bridge's media server, or nil if direct
// media is off. Nothing is downloaded until the homeserver asks for the image.
func (fc *FChatConnector) directAvatar(avatarID networkid.AvatarID) *bridgev2.Avatar {
	mediaID := urlMediaID(string(avatarID))
	mxc := fc.directMXC(context.Background(), mediaID)
	if mxc == "" {
		return nil
	}
	return &bridgev2.Avatar{ID: avatarID, MXC: mxc, Hash: sha256.Sum256(mediaID)}
}

var errUnknownMedia = mediaproxy.ErrInvalidMediaIDSyntax

// Download is called by the bridge's media server when the homeserver wants an image.
func (fc *FChatConnector) Download(ctx context.Context, mediaID networkid.MediaID, params map[string]string) (mediaproxy.GetMediaResponse, error) {
	if len(mediaID) < 2 {
		return nil, errUnknownMedia
	}
	payload := string(mediaID[1:])
	switch mediaID[0] {
	case mediaKindURL:
		return fc.downloadStatic(ctx, payload)
	case mediaKindEIcons:
		names := strings.Split(payload, "|")
		if len(names) > maxCombinedEIcons {
			return nil, errUnknownMedia
		}
		ctx, cancel := context.WithTimeout(ctx, imageTimeout)
		defer cancel()
		data, mimeType, _, _, _, err := fc.renderCombinedEIcons(ctx, names)
		if err != nil {
			return nil, err
		}
		return &mediaproxy.GetMediaResponseData{
			Reader:        io.NopCloser(bytes.NewReader(data)),
			ContentType:   mimeType,
			ContentLength: int64(len(data)),
		}, nil
	}
	return nil, errUnknownMedia
}

// downloadStatic streams an image from the F-List static server.
func (fc *FChatConnector) downloadStatic(ctx context.Context, url string) (mediaproxy.GetMediaResponse, error) {
	url, _, _ = strings.Cut(url, "#")
	// The media IDs are signed by the bridge, this only guards against bugs.
	if !strings.HasPrefix(url, strings.TrimSuffix(fc.Config.StaticURL, "/")+"/") {
		return nil, errUnknownMedia
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fc.userAgent())
	resp, err := fc.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d downloading %s", resp.StatusCode, url)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.HasPrefix(contentType, "image/") {
		_ = resp.Body.Close()
		return nil, errors.New("downloaded file isn't an image")
	}
	return &mediaproxy.GetMediaResponseData{
		Reader:        resp.Body,
		ContentType:   contentType,
		ContentLength: max(resp.ContentLength, 0),
	}, nil
}
