package connector

import (
	_ "embed"
	"fmt"
	"net/url"
	"strings"
	"time"

	up "go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	ServerURL string `yaml:"server_url"`
	SiteURL   string `yaml:"site_url"`
	StaticURL string `yaml:"static_url"`

	ErrorWaitMS     int  `yaml:"error_wait_ms"`
	MemberSyncLimit int  `yaml:"member_sync_limit"`
	BridgeAds       bool `yaml:"bridge_ads"`

	CreateFriendDMs      bool `yaml:"create_friend_dms"`
	QueueOfflineMessages bool `yaml:"queue_offline_messages"`

	BridgeStatusChanges bool `yaml:"bridge_status_changes"`
	BridgeNotices       bool `yaml:"bridge_notices"`

	DefaultStatusMessage string `yaml:"default_status_message"`
	AutoIdleMinutes      int    `yaml:"auto_idle_minutes"`
	OnlineAlerts         bool   `yaml:"online_alerts"`

	FetchCharacterProfiles bool `yaml:"fetch_character_profiles"`
	BeeperProfileFields    bool `yaml:"beeper_profile_fields"`
	GhostCleanupDays       int  `yaml:"ghost_cleanup_days"`
}

func (c *Config) ErrorWait() time.Duration {
	if c.ErrorWaitMS <= 0 {
		return time.Second
	}
	return time.Duration(c.ErrorWaitMS) * time.Millisecond
}

// ShouldSyncMembers returns whether a channel with the given member count gets its member list synced.
func (c *Config) ShouldSyncMembers(count int) bool {
	switch {
	case c.MemberSyncLimit < 0:
		return false
	case c.MemberSyncLimit == 0:
		return true
	default:
		return count <= c.MemberSyncLimit
	}
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "server_url")
	helper.Copy(up.Str, "site_url")
	helper.Copy(up.Str, "static_url")
	helper.Copy(up.Int, "error_wait_ms")
	helper.Copy(up.Int, "member_sync_limit")
	helper.Copy(up.Bool, "bridge_ads")
	helper.Copy(up.Bool, "create_friend_dms")
	helper.Copy(up.Bool, "queue_offline_messages")
	helper.Copy(up.Bool, "bridge_status_changes")
	helper.Copy(up.Bool, "bridge_notices")
	helper.Copy(up.Str, "default_status_message")
	helper.Copy(up.Int, "auto_idle_minutes")
	helper.Copy(up.Bool, "online_alerts")
	helper.Copy(up.Bool, "fetch_character_profiles")
	helper.Copy(up.Bool, "beeper_profile_fields")
	helper.Copy(up.Int, "ghost_cleanup_days")
}

func (fc *FChatConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &fc.Config, &up.StructUpgrader{
		SimpleUpgrader: upgradeConfig,
		Blocks: [][]string{
			{"error_wait_ms"},
			{"member_sync_limit"},
			{"bridge_ads"},
			{"create_friend_dms"},
			{"queue_offline_messages"},
			{"bridge_status_changes"},
			{"default_status_message"},
			{"auto_idle_minutes"},
			{"online_alerts"},
			{"fetch_character_profiles"},
			{"beeper_profile_fields"},
			{"ghost_cleanup_days"},
		},
		Base: ExampleConfig,
	}
}

func validateURL(field, value string, schemes ...string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}
	for _, scheme := range schemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			return nil
		}
	}
	return fmt.Errorf("invalid %s: scheme must be one of %v", field, schemes)
}

func (fc *FChatConnector) ValidateConfig() error {
	if err := validateURL("server_url", fc.Config.ServerURL, "ws", "wss"); err != nil {
		return err
	}
	if err := validateURL("site_url", fc.Config.SiteURL, "http", "https"); err != nil {
		return err
	}
	if err := validateURL("static_url", fc.Config.StaticURL, "http", "https"); err != nil {
		return err
	}
	return nil
}
