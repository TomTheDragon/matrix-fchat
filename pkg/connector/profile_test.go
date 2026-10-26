package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"matrix-fchat/pkg/fchat"
)

func TestPronounsFor(t *testing.T) {
	for gender, want := range map[string]string{
		"Male": "he/him", "female": "she/her", "Herm": "they/them",
		"Cunt-boy": "they/them", "None": "they/them", "": "",
	} {
		if got := pronounsFor(gender); got != want {
			t.Errorf("pronounsFor(%q) = %q, want %q", gender, got, want)
		}
	}
}

func TestProfileSummary(t *testing.T) {
	data := &fchat.CharacterData{Infotags: map[string]string{
		infotagGender: "2", infotagSpecies: "Wolf", infotagAge: "25",
		infotagOrientation: "30", infotagRole: "40", infotagLookingFor: "Long  [b]stories[/b] &amp; more",
	}}
	items := map[string]string{"2": "Female", "30": "Bisexual", "40": "Switch"}
	want := "Female · Wolf · Age 25 · Bisexual · Switch\nLooking for: Long stories & more"
	if got := profileSummary(data, items); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := profileSummary(&fchat.CharacterData{}, items); got != "" {
		t.Errorf("empty profile gave %q", got)
	}
}

func TestProfileFields(t *testing.T) {
	c := &FChatClient{characters: map[networkid.UserID]*characterInfo{
		"zed": {Name: "Zed", Status: fchat.StatusLooking, StatusMsg: "[b]LF[/b] scenes"},
	}}
	meta := &GhostMetadata{Gender: "Female", ProfileSummary: "Female · Wolf"}
	fields := c.profileFields("zed", meta)
	get := func(key string) string { return string(fields[key]) }
	if get("m.pronouns") != `[{"language":"en","summary":"she/her"}]` || get("io.fsky.nyx.pronouns") != get("m.pronouns") {
		t.Errorf("unexpected pronouns %s", get("m.pronouns"))
	}
	if get("moe.sable.app.name_color") != `"#ff6699"` {
		t.Errorf("unexpected color %s", get("moe.sable.app.name_color"))
	}
	var bio struct {
		Text []struct{ Body, Mimetype string } `json:"m.text"`
	}
	if err := json.Unmarshal(fields["m.biography"], &bio); err != nil || len(bio.Text) != 2 {
		t.Fatalf("unexpected biography %s (%v)", get("m.biography"), err)
	}
	if bio.Text[1].Body != "Female · Wolf\nStatus: looking – LF scenes" || bio.Text[0].Mimetype != "text/html" ||
		!strings.Contains(bio.Text[0].Body, "<br>") {
		t.Errorf("unexpected biography %+v", bio.Text)
	}
	if get("moe.sable.app.bio") != `"Female · Wolf<br>Status: looking – LF scenes"` {
		t.Errorf("unexpected Sable bio %s", get("moe.sable.app.bio"))
	}

	// Unknown gender and no bio: the fields are removed.
	fields = c.profileFields("nobody", &GhostMetadata{})
	for _, key := range allProfileKeys {
		if string(fields[key]) != "null" {
			t.Errorf("%s should be null, got %s", key, fields[key])
		}
	}
}
