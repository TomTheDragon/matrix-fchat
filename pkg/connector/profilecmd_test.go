package connector

import (
	"strings"
	"testing"

	"matrix-fchat/pkg/fchat"
)

func TestProfileOverview(t *testing.T) {
	view := &profileView{
		Name: "Zed", Profile: "https://www.f-list.net/c/Zed", Chat: "https://matrix.to/#/%23x:hs",
		Gender: "Female", Status: fchat.StatusLooking, StatusMsg: "[b]LF[/b] scenes",
		Data: &fchat.CharacterData{
			CustomTitle: "The Wolf", Description: "[b]Hello[/b] there", Views: 42, CreatedAt: 1500000000,
			Infotags:    map[string]string{infotagGender: "2", infotagSpecies: "Wolf", "25": "7", "83": "zed#1"},
			Kinks:       map[string]string{"1": "fave", "2": "yes", "3": "no"},
			CustomKinks: []fchat.CustomKink{{Name: "Hugs", Choice: "fave"}},
		},
		ListItems: map[string]string{"2": "Female", "7": "Long"},
		KinkNames: map[string]string{"1": "Biting", "2": "Cuddles"},
		Infotags: []profileInfotag{
			{ID: "83", Name: "Discord", Group: "1"},
			{ID: infotagGender, Name: "Gender", IsList: true, Group: "3"},
			{ID: infotagSpecies, Name: "Species", Group: "3"},
			{ID: "25", Name: "Desired RP length", IsList: true, Group: "5"},
			{ID: "12", Name: "Height/Length", Group: "3"},
		},
		Groups: map[string]string{"1": "Contact details/Sites", "3": "General details", "5": "RPing preferences"},
	}
	content := view.render()
	want := "Zed – The Wolf (https://www.f-list.net/c/Zed)\nOnline: looking – LF scenes\n\n" +
		"General details\nGender: Female\nSpecies: Wolf\n\n" +
		"RPing preferences\nDesired RP length: Long\n\n" +
		"Contact details/Sites\nDiscord: zed#1\n\n" +
		"Kinks\n2 favorite, 1 yes, 0 maybe, 1 no\nFavorites: Biting, Hugs\nYes: Cuddles\n\n" +
		"> Hello there\n\nProfile created 2017-07-14, 42 views"
	if content.Body != want {
		t.Errorf("unexpected body:\n%s\n\nwant:\n%s", content.Body, want)
	}
	if !strings.Contains(content.FormattedBody, "💬") || !strings.Contains(content.FormattedBody, "<blockquote>Hello there</blockquote>") ||
		strings.Contains(content.FormattedBody, "Full profile") {
		t.Errorf("unexpected HTML %s", content.FormattedBody)
	}
}

func TestLimitList(t *testing.T) {
	if got := limitList([]string{"a", "b", "c"}, 2); got != "a, b and 1 more" {
		t.Errorf("got %q", got)
	}
}

func TestProfileOverviewMemoAndMatch(t *testing.T) {
	view := &profileView{
		Name: "Zed", Profile: "https://www.f-list.net/c/Zed",
		Data:  &fchat.CharacterData{},
		Memo:  "Met in <Frontpage>\nlikes tea",
		Match: &kinkMatch{Own: "Me", Shared: 3, Conflicts: 1, BothFavorites: []string{"Biting"}},
	}
	content := view.render()
	for _, want := range []string{"Kink match with Me: 75% (3 liked by both, 1 conflicts)", "Favorites of both: Biting",
		"Your memo: Met in <Frontpage>\nlikes tea"} {
		if !strings.Contains(content.Body, want) {
			t.Errorf("body misses %q:\n%s", want, content.Body)
		}
	}
	if !strings.Contains(content.FormattedBody, "Met in &lt;Frontpage&gt;<br>likes tea") {
		t.Errorf("memo not escaped in HTML: %s", content.FormattedBody)
	}
}
