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
	m.InitVersion(Tag, Commit, BuildTime)
	fc.Version = m.Version
	m.Run()
}
