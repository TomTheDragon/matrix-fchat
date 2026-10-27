package connector

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// mautrix sends a "Private chat portal created" notice when the user starts a private chat by
// inviting a ghost. There's no option to turn it off, so the bridge drops it on its way to the
// homeserver: the appservice HTTP client gets a transport that answers that one request itself.

// droppedNotices are the bodies of the notices that are never sent.
var droppedNotices = map[string]bool{
	"Private chat portal created": true,
}

type noticeFilterTransport struct {
	base http.RoundTripper
}

// isDroppedNotice returns whether a request sends one of the dropped notices.
func isDroppedNotice(req *http.Request, body []byte) bool {
	if req.Method != http.MethodPut || !strings.Contains(req.URL.Path, "/send/m.room.message/") {
		return false
	}
	var content struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	}
	return json.Unmarshal(body, &content) == nil && content.MsgType == "m.notice" && droppedNotices[content.Body]
}

func (t *noticeFilterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPut || req.Body == nil || !strings.Contains(req.URL.Path, "/send/m.room.message/") {
		return t.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	if isDroppedNotice(req, body) {
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{"Content-Type": {"application/json"}},
			Body:          io.NopCloser(strings.NewReader(`{"event_id":"$dropped-notice"}`)),
			ContentLength: -1,
			Request:       req,
		}, nil
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return t.base.RoundTrip(req)
}

// SetupNoticeFilter makes the appservice drop unnecessary notices from mautrix.
// It has to be called before the bridge starts.
func (fc *FChatConnector) SetupNoticeFilter() {
	mc := fc.matrixConnector()
	if mc == nil || mc.AS == nil || mc.AS.HTTPClient == nil {
		return
	}
	base := mc.AS.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*noticeFilterTransport); ok {
		return
	}
	mc.AS.HTTPClient.Transport = &noticeFilterTransport{base: base}
}
