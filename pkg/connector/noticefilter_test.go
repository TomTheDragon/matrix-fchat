package connector

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type recordingTransport struct{ bodies []string }

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	rt.bodies = append(rt.bodies, string(body))
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"event_id":"$real"}`)), Request: req}, nil
}

func TestNoticeFilter(t *testing.T) {
	rec := &recordingTransport{}
	client := &http.Client{Transport: &noticeFilterTransport{base: rec}}
	send := func(body string) string {
		req, _ := http.NewRequest(http.MethodPut, "https://hs/_matrix/client/v3/rooms/!r:hs/send/m.room.message/txn1", strings.NewReader(body))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		return string(data)
	}
	if got := send(`{"msgtype":"m.notice","body":"Private chat portal created"}`); !strings.Contains(got, "$dropped-notice") {
		t.Errorf("notice wasn't dropped: %s", got)
	}
	if got := send(`{"msgtype":"m.text","body":"Private chat portal created"}`); !strings.Contains(got, "$real") {
		t.Errorf("text message was dropped: %s", got)
	}
	if got := send(`{"msgtype":"m.notice","body":"Private chat portal created\n\nWarning: x"}`); !strings.Contains(got, "$real") {
		t.Errorf("warning was dropped: %s", got)
	}
	if len(rec.bodies) != 2 || !strings.Contains(rec.bodies[0], "m.text") {
		t.Errorf("unexpected forwarded bodies %q", rec.bodies)
	}
}
