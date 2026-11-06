// Package connector implements the bridgev2 network connector for F-Chat.
package connector

import (
	"context"
	"net/http"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/fchat"
)

// ClientName is sent to the F-Chat server in the IDN command.
const ClientName = "matrix-fchat bridge"

// NetworkIcon is the default avatar of the bridge bot, the per-login spaces and the management room.
const NetworkIcon id.ContentURIString = "mxc://foxyden.eu/AJenOiLFKYmxwrQgmGbrvbpP"

type FChatConnector struct {
	Bridge *bridgev2.Bridge
	Config Config
	// openTokens caches the payloads of short open link tokens (token -> payload).
	openTokens sync.Map

	// Version is reported to the F-Chat server. It's set by main.
	Version string

	API     *fchat.API
	Tickets *fchat.TicketCache
	HTTP    *http.Client

	images        imageCache
	directMedia   bool
	searchOptions searchOptionsCache
	profiles      profileFetcher
	presence      presenceTracker
}

var (
	_ bridgev2.NetworkConnector            = (*FChatConnector)(nil)
	_ bridgev2.ConfigValidatingNetwork     = (*FChatConnector)(nil)
	_ bridgev2.IdentifierValidatingNetwork = (*FChatConnector)(nil)
)

func (fc *FChatConnector) Init(bridge *bridgev2.Bridge) {
	fc.Bridge = bridge
	fc.HTTP = &http.Client{Timeout: 60 * time.Second}
	fc.API = &fchat.API{
		SiteURL:   fc.Config.SiteURL,
		UserAgent: fc.userAgent(),
		HTTP:      fc.HTTP,
	}
	fc.Tickets = fchat.NewTicketCache(fc.API)
	if proc, ok := bridge.Commands.(*commands.Processor); ok {
		proc.AddHandlers(fc.commandHandlers()...)
	}
}

func (fc *FChatConnector) userAgent() string {
	version := fc.Version
	if version == "" {
		version = "dev"
	}
	return "matrix-fchat/" + version + " (+mautrix bridgev2)"
}

func (fc *FChatConnector) Start(ctx context.Context) error {
	fc.startPresence(ctx)
	fc.startGhostCleanup()
	fc.startTimers()
	return nil
}

func (fc *FChatConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "F-Chat",
		NetworkURL:           "https://www.f-list.net",
		NetworkIcon:          NetworkIcon,
		NetworkID:            "fchat",
		BeeperBridgeType:     "matrix-fchat",
		DefaultPort:          29380,
		DefaultCommandPrefix: "!fchat",
	}
}

func (fc *FChatConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		// Check ghost info on every message. Without this, ghosts that got a name
		// without an avatar would never get their avatar.
		AggressiveUpdateInfo: true,
		Provisioning: bridgev2.ProvisioningCapabilities{
			ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
				CreateDM:       true,
				LookupUsername: true,
				ContactList:    true,
				Search:         true,
			},
		},
	}
}

func (fc *FChatConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 1
}

func (fc *FChatConnector) ValidateUserID(id networkid.UserID) bool {
	return isValidCharacterName(string(id))
}

func (fc *FChatConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	login.Client = fc.newClient(login)
	return nil
}
