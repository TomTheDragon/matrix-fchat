package connector

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"matrix-fchat/pkg/fchat"
)

// Open links are links to room aliases like #fchat_open_<payload>_<signature>:example.com.
// The aliases don't exist until someone opens the link: the homeserver then asks the bridge
// about the alias, and the bridge joins the channel (or creates the DM room), invites the
// user and creates the alias. This makes it possible to join channels and start chats
// with a click, which Matrix links can't do otherwise.
//
// The payload contains the login and the target, and is signed so that nobody else
// can make a login join channels.

const (
	openAliasPrefix = "fchat_open_"
	openKindChannel = 'c'
	openKindDM      = 'd'
	// openAliasLifetime is how long a created alias exists. It's deleted afterwards, so
	// that it doesn't point to an old room when the channel is left.
	openAliasLifetime = 5 * time.Minute
	openRoomTimeout   = 45 * time.Second
)

var openEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func (fc *FChatConnector) matrixConnector() *matrix.Connector {
	if fc.Bridge == nil {
		return nil
	}
	mc, _ := fc.Bridge.Matrix.(*matrix.Connector)
	return mc
}

// openLinkKey is the key for signing open links. It's derived from the appservice token,
// which only the bridge and the homeserver know.
func (fc *FChatConnector) openLinkKey() []byte {
	mc := fc.matrixConnector()
	if mc == nil || mc.Config == nil || mc.Config.AppService.ASToken == "" {
		return nil
	}
	sum := sha256.Sum256([]byte("matrix-fchat open links\x00" + mc.Config.AppService.ASToken))
	return sum[:]
}

func signOpenPayload(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil)[:10]
}

func makeOpenAliasLocalpart(key []byte, kind byte, login networkid.UserLoginID, target string) string {
	payload := []byte(string(kind) + string(login) + "\x00" + target)
	return openAliasPrefix +
		strings.ToLower(openEncoding.EncodeToString(payload)) + "_" +
		strings.ToLower(openEncoding.EncodeToString(signOpenPayload(key, payload)))
}

func parseOpenAliasLocalpart(key []byte, localpart string) (kind byte, login networkid.UserLoginID, target string, ok bool) {
	rest, ok := strings.CutPrefix(localpart, openAliasPrefix)
	if !ok {
		return
	}
	encPayload, encSig, ok := strings.Cut(rest, "_")
	if !ok {
		return
	}
	payload, err1 := openEncoding.DecodeString(strings.ToUpper(encPayload))
	sig, err2 := openEncoding.DecodeString(strings.ToUpper(encSig))
	if err1 != nil || err2 != nil || len(payload) < 2 || !hmac.Equal(sig, signOpenPayload(key, payload)) {
		return 0, "", "", false
	}
	loginPart, target, ok := strings.Cut(string(payload[1:]), "\x00")
	if !ok || loginPart == "" || target == "" {
		return 0, "", "", false
	}
	return payload[0], networkid.UserLoginID(loginPart), target, true
}

// openLink returns a link that opens a channel or DM for the login, or "" if
// open links aren't available.
func (fc *FChatConnector) openLink(login networkid.UserLoginID, kind byte, target string) string {
	key := fc.openLinkKey()
	if key == nil || target == "" {
		return ""
	}
	alias := "#" + makeOpenAliasLocalpart(key, kind, login, target) + ":" + fc.Bridge.Matrix.ServerName()
	return openLinkURL(alias)
}

// openLinkURL makes a matrix.to link to the alias.
func openLinkURL(alias string) string {
	return "https://matrix.to/#/" + alias
}

// channelLink returns a link that joins a channel and opens its room.
func (c *FChatClient) channelLink(channel string) string {
	return c.Main.openLink(c.UserLogin.ID, openKindChannel, channel)
}

// dmLink returns a link that opens the DM room with a character.
func (c *FChatClient) dmLink(name string) string {
	if makeUserID(name) == c.ownID {
		return ""
	}
	return c.Main.openLink(c.UserLogin.ID, openKindDM, name)
}

// QueryAlias is called by the homeserver when someone resolves an alias in the bridge's namespace.
func (fc *FChatConnector) QueryAlias(alias id.RoomAlias) bool {
	ctx := fc.Bridge.BackgroundCtx
	if ctx == nil {
		ctx = context.Background()
	}
	log := fc.Bridge.Log.With().Str("action", "query alias").Stringer("alias", alias).Logger()
	ctx, cancel := context.WithTimeout(log.WithContext(ctx), openRoomTimeout)
	defer cancel()

	mc := fc.matrixConnector()
	key := fc.openLinkKey()
	localpart, server, found := strings.Cut(strings.TrimPrefix(string(alias), "#"), ":")
	if mc == nil || key == nil || !found || server != fc.Bridge.Matrix.ServerName() {
		return false
	}
	kind, loginID, target, ok := parseOpenAliasLocalpart(key, localpart)
	if !ok {
		log.Debug().Msg("Invalid or unsigned open alias")
		return false
	}
	login := fc.Bridge.GetCachedUserLoginByID(loginID)
	if login == nil {
		log.Debug().Str("login_id", string(loginID)).Msg("Login of open alias not found")
		return false
	}
	client, ok := login.Client.(*FChatClient)
	if !ok {
		return false
	}
	roomID, err := client.openRoom(ctx, kind, target)
	if err != nil {
		log.Warn().Err(err).Str("target", target).Msg("Failed to open room for alias")
		return false
	}
	if err = fc.Bridge.Bot.EnsureInvited(ctx, roomID, login.UserMXID); err != nil {
		log.Warn().Err(err).Msg("Failed to invite user to opened room")
	}
	if _, err = mc.Bot.CreateAlias(ctx, alias, roomID); err != nil {
		log.Err(err).Msg("Failed to create alias")
		return false
	}
	log.Debug().Stringer("room_id", roomID).Msg("Created open alias")
	go func() {
		time.Sleep(openAliasLifetime)
		if _, err := mc.Bot.DeleteAlias(context.Background(), alias); err != nil {
			log.Warn().Err(err).Msg("Failed to delete open alias")
		}
	}()
	return true
}

// QueryUser is called by the homeserver for unknown users in the bridge's namespace.
func (fc *FChatConnector) QueryUser(userID id.UserID) bool {
	return false
}

var _ appservice.QueryHandler = (*FChatConnector)(nil)

// openRoom returns the room of a channel or DM, joining the channel or creating the room first.
func (c *FChatClient) openRoom(ctx context.Context, kind byte, target string) (id.RoomID, error) {
	var portalKey networkid.PortalKey
	switch kind {
	case openKindChannel:
		portalKey = makeChannelPortalKey(target, c.UserLogin.ID)
	case openKindDM:
		if !isValidCharacterName(target) || makeUserID(target) == c.ownID {
			return "", fmt.Errorf("invalid DM target %q", target)
		}
		portalKey = makeDMPortalKey(target, c.UserLogin.ID)
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		return "", err
	} else if portal != nil && portal.MXID != "" {
		return portal.MXID, nil
	}

	switch kind {
	case openKindChannel:
		if !c.Conn.IsConnected() {
			return "", fchat.ErrNotConnected
		}
		// The portal is created when the server confirms the join.
		if err = c.Conn.Send(ctx, fchat.CmdJoinChannel, &fchat.ChannelRequest{Channel: target}); err != nil {
			return "", err
		}
	case openKindDM:
		c.queueDMCreation(target)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("room wasn't created in time: %w", ctx.Err())
		case <-ticker.C:
		}
		portal, err = c.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
		if err != nil {
			return "", err
		} else if portal != nil && portal.MXID != "" {
			return portal.MXID, nil
		}
	}
}

// SetupOpenLinks makes the bridge answer alias queries from the homeserver and adds the
// alias namespace to the registration file if it's missing. It must be called after the
// bridge was initialized.
func (fc *FChatConnector) SetupOpenLinks(registrationPath string) {
	mc := fc.matrixConnector()
	if mc == nil {
		return
	}
	mc.AS.QueryHandler = fc
	log := fc.Bridge.Log.With().Str("component", "open links").Logger()
	changed, err := ensureAliasNamespace(registrationPath, OpenAliasRegex(mc.Config.Homeserver.Domain))
	if err != nil {
		log.Warn().Err(err).Str("path", registrationPath).
			Msg("Failed to check the alias namespace in the registration, links to join channels may not work")
	} else if changed {
		log.Warn().Str("path", registrationPath).
			Msg("Added the alias namespace to the registration file. Restart the homeserver (or reload the registration) so that links to join channels and open chats work.")
	}
}

// OpenAliasRegex is the alias namespace of the open links in the registration.
func OpenAliasRegex(domain string) string {
	return fmt.Sprintf("^#%s[a-z0-9_]+:%s$", openAliasPrefix, regexp.QuoteMeta(domain))
}

func yamlMapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func yamlMapSet(m *yaml.Node, key string, value *yaml.Node) {
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// ensureAliasNamespace adds an exclusive alias namespace to a registration file,
// keeping everything else in the file as it is. A missing file is ignored.
func ensureAliasNamespace(path, regex string) (bool, error) {
	if path == "" {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false, errors.New("registration is not a YAML mapping")
	}
	root := doc.Content[0]
	namespaces := yamlMapGet(root, "namespaces")
	if namespaces == nil {
		namespaces = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlMapSet(root, "namespaces", namespaces)
	} else if namespaces.Kind != yaml.MappingNode {
		return false, errors.New("namespaces is not a mapping")
	}
	aliases := yamlMapGet(namespaces, "aliases")
	if aliases == nil || aliases.Tag == "!!null" {
		if aliases == nil {
			aliases = &yaml.Node{}
			yamlMapSet(namespaces, "aliases", aliases)
		}
		*aliases = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	} else if aliases.Kind != yaml.SequenceNode {
		return false, errors.New("namespaces.aliases is not a list")
	}
	for _, item := range aliases.Content {
		if r := yamlMapGet(item, "regex"); r != nil && r.Value == regex {
			return false, nil
		}
	}
	aliases.Content = append(aliases.Content, &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "regex"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: regex},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "exclusive"},
			{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
		},
	})
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(4)
	if err = enc.Encode(&doc); err != nil {
		return false, err
	}
	if err = enc.Close(); err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if err = os.WriteFile(path, out.Bytes(), info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}
