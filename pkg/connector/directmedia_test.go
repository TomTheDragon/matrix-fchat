package connector

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"
)

func TestDownloadRejectsUnknownMedia(t *testing.T) {
	fc := &FChatConnector{Config: Config{StaticURL: "https://static.f-list.net"}}
	for _, mediaID := range []networkid.MediaID{
		nil,
		networkid.MediaID("x"),
		networkid.MediaID("zsomething"),
		urlMediaID("https://example.com/images/avatar/foo.png"),
		urlMediaID("https://static.f-list.net.example.com/x.png"),
	} {
		if _, err := fc.Download(context.Background(), mediaID, nil); !errors.Is(err, mediaproxy.ErrInvalidMediaIDSyntax) {
			t.Errorf("Download(%q) = %v, want invalid media ID", mediaID, err)
		}
	}
}

func TestDirectMXCDisabled(t *testing.T) {
	fc := &FChatConnector{}
	if mxc := fc.directMXC(context.Background(), urlMediaID("https://static.f-list.net/x.png")); mxc != "" {
		t.Errorf("directMXC without direct media = %q, want empty", mxc)
	}
	if avatar := fc.directAvatar("https://static.f-list.net/x.png#1"); avatar != nil {
		t.Errorf("directAvatar without direct media = %v, want nil", avatar)
	}
}
