package connector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"

	"matrix-fchat/pkg/fchat"
)

const (
	// minAutoAdMinutes is the shortest interval: F-Chat allows one ad per channel every 10 minutes.
	minAutoAdMinutes = 10
	maxAutoAdMinutes = 24 * 60
	// autoAdDuration is how long an ad is posted before it stops, so that ads of characters
	// nobody is watching don't run forever.
	autoAdDuration = 3 * time.Hour
)

// autoAd is an ad that the bridge posts into a channel again and again.
type autoAd struct {
	Text string `json:"text"`
	// Interval in minutes.
	Interval int `json:"interval"`
	// Started and LastPosted are unix seconds.
	Started    int64 `json:"started"`
	LastPosted int64 `json:"last_posted"`
}

func (ad *autoAd) expires() time.Time {
	return time.Unix(ad.Started, 0).Add(autoAdDuration)
}

func (ad *autoAd) next() time.Time {
	return time.Unix(ad.LastPosted, 0).Add(time.Duration(ad.Interval) * time.Minute)
}

// postAd posts an ad into a channel. The server doesn't echo ads back to the sender, so the
// bridge shows it in the room itself if ads are shown there. It returns whether it was shown.
func (c *FChatClient) postAd(ctx context.Context, channel, text string) (bool, error) {
	err := c.Conn.SendMessage(ctx, fchat.CmdAd, &fchat.ChannelMessage{Channel: channel, Message: text})
	if err != nil {
		return false, err
	}
	if !c.showAds(ctx, channel) {
		return false, nil
	}
	c.queueMessage(makeChannelPortalKey(channel, c.UserLogin.ID), c.meta().Character, c.adMessage(text), false)
	return true, nil
}

// postAutoAds posts the ads that are due and stops the expired ones.
func (c *FChatClient) postAutoAds() {
	if !c.autoAdLock.TryLock() {
		return
	}
	defer c.autoAdLock.Unlock()
	ctx := c.ctx()
	now := time.Now()
	type due struct {
		channel string
		ad      autoAd
	}
	var dueAds []due
	var expired []string
	c.metaLock.Lock()
	for channel, ad := range c.meta().AutoAds {
		switch {
		case now.After(ad.expires()):
			expired = append(expired, channel)
		case !now.Before(ad.next()):
			dueAds = append(dueAds, due{channel, *ad})
		}
	}
	c.metaLock.Unlock()
	// Ads are only posted while the character is in the channel (e.g. not while rejoining).
	dueAds = slices.DeleteFunc(dueAds, func(item due) bool { return !c.isInChannel(item.channel) })
	slices.SortFunc(dueAds, func(a, b due) int { return strings.Compare(a.channel, b.channel) })

	changed := false
	for _, channel := range expired {
		c.stopAutoAd(channel)
		changed = true
		c.sendPortalNotice(channel, fmt.Sprintf(
			"The automatic ad stopped after %d hours. Start it again with `%s auto-ad <minutes> <text>`.",
			int(autoAdDuration.Hours()), c.Main.Bridge.Config.CommandPrefix))
	}
	for _, item := range dueAds {
		_, err := c.postAd(ctx, item.channel, item.ad.Text)
		var fcErr *fchat.Error
		if err != nil && (!errors.As(err, &fcErr) || fcErr.Code == fchat.ErrCodeMessageFlood) {
			// Try again next time.
			c.log.Debug().Err(err).Str("channel", item.channel).Msg("Failed to post automatic ad")
			continue
		}
		c.metaLock.Lock()
		if ad, ok := c.meta().AutoAds[item.channel]; ok {
			if err != nil {
				delete(c.meta().AutoAds, item.channel)
			} else {
				ad.LastPosted = now.Unix()
			}
		}
		c.metaLock.Unlock()
		changed = true
		if err != nil {
			c.sendPortalNotice(item.channel, "The automatic ad was stopped: "+fcErr.Message)
		}
	}
	if changed {
		if err := c.UserLogin.Save(ctx); err != nil {
			c.log.Err(err).Msg("Failed to save login after posting automatic ads")
		}
	}
}

// stopAutoAd removes a channel's auto ad and returns whether there was one.
func (c *FChatClient) stopAutoAd(channel string) bool {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	_, ok := c.meta().AutoAds[channel]
	delete(c.meta().AutoAds, channel)
	return ok
}

const autoAdUsage = "**Usage:** `$cmdprefix auto-ad <minutes> <text>` posts the ad now and then every N minutes " +
	"(at least 10) for 3 hours. `$cmdprefix auto-ad stop` stops it, `$cmdprefix auto-ad` shows the ads that are running."

func (fc *FChatConnector) cmdAutoAd(ce *commands.Event) {
	client := fc.clientFor(ce)
	if client == nil {
		return
	}
	if len(ce.Args) == 0 {
		ce.Reply(client.describeAutoAds())
		return
	}
	channel := portalChannel(ce)
	if channel == "" {
		return
	}
	if strings.EqualFold(ce.Args[0], "stop") || strings.EqualFold(ce.Args[0], "off") {
		if !client.stopAutoAd(channel) {
			ce.Reply("No automatic ad is running in this channel")
			return
		}
		if err := client.UserLogin.Save(ce.Ctx); err != nil {
			ce.Reply("Failed to save: %v", err)
			return
		}
		ce.Reply("Stopped the automatic ad")
		return
	}
	minutes, err := strconv.Atoi(ce.Args[0])
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ce.RawArgs), ce.Args[0]))
	if err != nil || text == "" {
		ce.Reply(autoAdUsage)
		return
	} else if minutes < minAutoAdMinutes || minutes > maxAutoAdMinutes {
		ce.Reply("The interval must be between %d and %d minutes", minAutoAdMinutes, maxAutoAdMinutes)
		return
	}
	client = fc.connectedClientFor(ce)
	if client == nil {
		return
	}
	if len(text) > client.Conn.AdMax() {
		ce.Reply("The ad is too long")
		return
	}
	client.autoAdLock.Lock()
	defer client.autoAdLock.Unlock()
	if _, err = client.postAd(ce.Ctx, channel, text); err != nil {
		ce.Reply("Failed to post ad: %v", err)
		return
	}
	now := time.Now().Unix()
	client.metaLock.Lock()
	meta := client.meta()
	if meta.AutoAds == nil {
		meta.AutoAds = make(map[string]*autoAd)
	}
	meta.AutoAds[channel] = &autoAd{Text: text, Interval: minutes, Started: now, LastPosted: now}
	err = client.UserLogin.Save(ce.Ctx)
	client.metaLock.Unlock()
	if err != nil {
		ce.Reply("The ad was posted, but the automatic ad couldn't be saved: %v", err)
		return
	}
	ce.Reply("Posted the ad. It's posted again every %d minutes for %d hours; stop it with `$cmdprefix auto-ad stop`.",
		minutes, int(autoAdDuration.Hours()))
}

// describeAutoAds lists the running auto ads.
func (c *FChatClient) describeAutoAds() string {
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	ads := c.meta().AutoAds
	if len(ads) == 0 {
		return "No automatic ads are running.\n\n" + autoAdUsage
	}
	channels := make([]string, 0, len(ads))
	for channel := range ads {
		channels = append(channels, channel)
	}
	slices.Sort(channels)
	var out strings.Builder
	out.WriteString("**Automatic ads:**\n\n")
	for _, channel := range channels {
		ad := ads[channel]
		title := channel
		if t := c.meta().Channels[channel]; t != "" {
			title = t
		}
		fmt.Fprintf(&out, "* **%s**: every %d minutes until %s UTC – %s\n",
			title, ad.Interval, ad.expires().UTC().Format("15:04"), truncateRunes(ad.Text, 80))
	}
	out.WriteString("\nStop one with `$cmdprefix auto-ad stop` in the channel's room.")
	return out.String()
}
