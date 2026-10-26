package connector

import (
	"slices"
	"strings"
	"testing"
)

func testSearchOptions() *searchOptions {
	return &searchOptions{
		Kinks: []searchKink{
			{ID: "8", Name: "Dirty Talking"},
			{ID: "531", Name: "Femboys"},
			{ID: "60", Name: "Robots, Cyborgs, Androids"},
			{ID: "61", Name: "Leash & Collar"},
			{ID: "70", Name: "Oral Sex"},
			{ID: "71", Name: "Anal Sex"},
		},
		Values: map[string][]string{
			"genders":    {"Male", "Female", "Male-Herm", "Herm", "Cunt-boy"},
			"furryprefs": {"No humans, just furry characters", "Furs and / or humans", "Furries ok, Humans Preferred"},
			"roles":      {"Always dominant", "Switch"},
		},
	}
}

func TestParseSearchQuery(t *testing.T) {
	opts := testSearchOptions()
	q, err := parseSearchQuery("kinks: dirty talk, Robots, Cyborgs, Androids, leash; genders: male, herm; furry: no humans, just furry characters; limit: 20", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.Request.Kinks, []string{"8", "60", "61"}) {
		t.Errorf("unexpected kinks %v", q.Request.Kinks)
	}
	if !slices.Equal(q.Request.Genders, []string{"Male", "Herm"}) {
		t.Errorf("unexpected genders %v", q.Request.Genders)
	}
	if !slices.Equal(q.Request.FurryPrefs, []string{"No humans, just furry characters"}) {
		t.Errorf("unexpected furry prefs %v", q.Request.FurryPrefs)
	}
	if q.Limit != 20 || strings.Contains(q.Text, "limit") {
		t.Errorf("unexpected limit %d / text %q", q.Limit, q.Text)
	}
	if q.describe() != "kinks: Dirty Talking, Robots, Cyborgs, Androids, Leash & Collar; genders: Male, Herm; furryprefs: No humans, just furry characters" {
		t.Errorf("unexpected description %q", q.describe())
	}

	for input, wantErr := range map[string]string{
		"genders: male":               "at least one kink",
		"kinks: sex":                  "matches several values",
		"kinks: nothing like this":    "unknown value",
		"kinks: femboys; colors: red": "unknown search field",
		"kinks femboys":               "missing `:`",
		"kinks: femboys; limit: 0":    "positive number",
		"kinks: dirty, femboys, robots, leash, oral, anal": "at most 5",
	} {
		if _, err := parseSearchQuery(input, opts); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("parseSearchQuery(%q): got error %v, want %q", input, err, wantErr)
		}
	}
}
