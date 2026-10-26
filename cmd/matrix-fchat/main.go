package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"matrix-fchat/pkg/connector"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag (see build.sh).
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	fc := &connector.FChatConnector{}
	m := mxmain.BridgeMain{
		Name:        "matrix-fchat",
		Description: "A Matrix-F-Chat puppeting bridge",
		URL:         "https://wiki.f-list.net/F-Chat_Protocol",
		Version:     "0.1.0",
		Connector:   fc,
	}
	m.PostInit = func() {
		// Use the F-Chat icon as the bot avatar when the config doesn't set one
		// (e.g. configs generated before the icon was added).
		bot := &m.Config.AppService.Bot
		if bot.Avatar == "" {
			bot.ParsedAvatar = connector.NetworkIcon.ParseOrIgnore()
		}
		// Links that join channels and open chats are room aliases that the bridge answers.
		fc.SetupOpenLinks(m.RegistrationPath)
	}
	m.InitVersion(Tag, Commit, BuildTime)
	fc.Version = m.Version
	m.Run()
}
