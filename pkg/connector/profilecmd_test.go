package connector

import (
	"strings"
	"testing"

	"matrix-fchat/pkg/fchat"
)

func TestProfileOverview(t *testing.T) {
	view := &profileView{
		Name: "Zed", Profile: "https://www.f-list.net/c/Zed", Chat: "https://app.element.io/#/room/%23x:hs",
		Gender: "Female", Status: fchat.StatusLooking, StatusMsg: "[b]LF[/b] scenes",
		Data: &fchat.CharacterData{
			CustomTitle: "The Wolf", Description: "[b]Hello[/b] there", Views: 42, CreatedAt: 1500000000,
			Infotags:    map[string]string{infotagGender: "2", infotagSpecies: "Wolf"},
			Kinks:       map[string]string{"1": "fave", "2": "yes", "3": "no"},
			CustomKinks: []fchat.CustomKink{{Name: "Hugs", Choice: "fave"}},
		},
		ListItems: map[string]string{"2": "Female"},
		KinkNames: map[string]string{"1": "Biting", "2": "Cuddles"},
	}
	content := view.render()
	for _, want := range []string{"Zed – The Wolf", "Online: looking – LF scenes", "Female · Wolf",
		"Kinks: 2 favorite, 1 yes, 0 maybe, 1 no", "Favorites: Biting, Hugs", "> Hello there",
		"Profile created 2017-07-14, 42 views", "https://www.f-list.net/c/Zed"} {
		if !strings.Contains(content.Body, want) {
			t.Errorf("body misses %q:\n%s", want, content.Body)
		}
	}
	if !strings.Contains(content.FormattedBody, "💬") || !strings.Contains(content.FormattedBody, "<blockquote>Hello there</blockquote>") {
		t.Errorf("unexpected HTML %s", content.FormattedBody)
	}
}
