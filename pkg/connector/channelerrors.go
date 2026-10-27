package connector

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"matrix-fchat/pkg/fchat"
)

// F-Chat's "channel not found" error (26) doesn't say which channel it's about. The bridge
// remembers the joins it sent, and when the error arrives, it waits a moment for the server
// to confirm the other joins. The joins that are still unconfirmed are the affected channels.

const (
	// channelErrorDelay is how long to wait after the last error before reporting.
	channelErrorDelay = 3 * time.Second
	// pendingJoinMaxAge is how long a join without an answer can still be blamed for an error.
	pendingJoinMaxAge = 2 * time.Minute
)

type pendingJoin struct {
	Channel string
	Sent    time.Time
}

type channelErrorState struct {
	lock sync.Mutex
	// pending are joins the server didn't confirm yet, by lowercase channel name.
	pending map[string]pendingJoin
	// errors is the number of "channel not found" errors since the last report.
	errors int
	timer  *time.Timer
}

// sendJoin joins a channel and remembers the join, so that errors can be matched to it.
func (c *FChatClient) sendJoin(ctx context.Context, channel string) error {
	c.channelErrs.lock.Lock()
	if c.channelErrs.pending == nil {
		c.channelErrs.pending = make(map[string]pendingJoin)
	}
	c.channelErrs.pending[strings.ToLower(channel)] = pendingJoin{Channel: channel, Sent: time.Now()}
	c.channelErrs.lock.Unlock()
	return c.Conn.Send(ctx, fchat.CmdJoinChannel, &fchat.ChannelRequest{Channel: channel})
}

// joinConfirmed is called when the server says the character is in a channel.
func (c *FChatClient) joinConfirmed(channel string) {
	c.channelErrs.lock.Lock()
	delete(c.channelErrs.pending, strings.ToLower(channel))
	c.channelErrs.lock.Unlock()
}

// handleChannelNotFound collects "channel not found" errors and reports them together.
func (c *FChatClient) handleChannelNotFound() {
	c.channelErrs.lock.Lock()
	defer c.channelErrs.lock.Unlock()
	c.channelErrs.errors++
	if c.channelErrs.timer == nil {
		c.channelErrs.timer = time.AfterFunc(channelErrorDelay, c.reportChannelErrors)
	} else {
		c.channelErrs.timer.Reset(channelErrorDelay)
	}
}

func (c *FChatClient) reportChannelErrors() {
	if c.rejoining.Load() {
		// More joins are still being sent, wait until they're answered.
		c.channelErrs.lock.Lock()
		c.channelErrs.timer.Reset(channelErrorDelay)
		c.channelErrs.lock.Unlock()
		return
	}
	c.channelErrs.lock.Lock()
	errorCount := c.channelErrs.errors
	c.channelErrs.errors = 0
	c.channelErrs.timer = nil
	var affected []string
	for key, join := range c.channelErrs.pending {
		delete(c.channelErrs.pending, key)
		if time.Since(join.Sent) < pendingJoinMaxAge {
			affected = append(affected, join.Channel)
		}
	}
	c.channelErrs.lock.Unlock()
	// Channels the character is in obviously exist.
	affected = slices.DeleteFunc(affected, func(channel string) bool {
		c.stateLock.RLock()
		defer c.stateLock.RUnlock()
		_, joined := c.channels[channel]
		return joined
	})
	slices.Sort(affected)

	// When there are as many errors as unconfirmed joins, the errors belong to those joins.
	// Otherwise, some joins failed for other reasons (or haven't been answered yet).
	certain := len(affected) > 0 && errorCount >= len(affected)
	if certain {
		for _, channel := range affected {
			c.saveChannelMeta(channel, "", false)
		}
	}
	c.sendManagementNotice(c.ctx(), formatChannelErrors(errorCount, affected, certain, c.channelTitle))
}

// channelTitle returns the known title of a channel, or "".
func (c *FChatClient) channelTitle(channel string) string {
	c.stateLock.RLock()
	if ch, ok := c.channels[channel]; ok && ch.Title != "" {
		c.stateLock.RUnlock()
		return ch.Title
	}
	c.stateLock.RUnlock()
	c.metaLock.Lock()
	defer c.metaLock.Unlock()
	return c.meta().Channels[channel]
}

func formatChannelErrors(errorCount int, affected []string, certain bool, title func(string) string) string {
	var out strings.Builder
	out.WriteString("**F-Chat error 26:** Could not locate the requested channel")
	if errorCount > 1 {
		fmt.Fprintf(&out, " (%d times)", errorCount)
	}
	out.WriteString(".")
	if len(affected) == 0 {
		out.WriteString(" The bridge couldn't tell which channel it was about.")
		return out.String()
	}
	if certain {
		out.WriteString("\n\nThese channels or rooms don't exist (anymore):\n\n")
	} else {
		out.WriteString("\n\nThe server didn't answer these joins, so they're probably affected:\n\n")
	}
	for _, channel := range affected {
		if t := title(channel); t != "" && t != channel {
			fmt.Fprintf(&out, "* %s (`%s`)\n", t, channel)
		} else {
			fmt.Fprintf(&out, "* `%s`\n", channel)
		}
	}
	if certain {
		out.WriteString("\nThe bridge won't try to rejoin them after reconnecting. " +
			"If one comes back, join it again with `$cmdprefix join <channel>`.")
	}
	return out.String()
}
