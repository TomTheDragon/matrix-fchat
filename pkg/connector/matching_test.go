package connector

import (
	"strings"
	"testing"
)

func TestMatchScoreRanksEvidence(t *testing.T) {
	few := kinkScore(&kinkMatch{Shared: 2})
	many := kinkScore(&kinkMatch{Shared: 40, Conflicts: 1})
	if few >= many {
		t.Errorf("2 shared kinks (%f) should rank below 40 shared with 1 conflict (%f)", few, many)
	}
	if kinkScore(&kinkMatch{Conflicts: 3}) != 0 {
		t.Error("no shared kinks should score 0")
	}
	if kinkScore(&kinkMatch{Shared: 10, Conflicts: 10}) >= kinkScore(&kinkMatch{Shared: 10, Conflicts: 1}) {
		t.Error("more conflicts should score lower")
	}
}

func TestFormatChannelErrors(t *testing.T) {
	title := func(ch string) string {
		if ch == "ADH-123" {
			return "My Room"
		}
		return ""
	}
	out := formatChannelErrors(2, []string{"ADH-123", "Gone"}, true, title)
	for _, want := range []string{"(2 times)", "* My Room (`ADH-123`)", "* `Gone`", "won't try to rejoin"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out = formatChannelErrors(1, []string{"A", "B"}, false, title)
	if !strings.Contains(out, "probably affected") || strings.Contains(out, "rejoin") {
		t.Errorf("unexpected uncertain report:\n%s", out)
	}
	out = formatChannelErrors(1, nil, false, title)
	if !strings.Contains(out, "couldn't tell") {
		t.Errorf("unexpected report without channels:\n%s", out)
	}
}

func TestSpaceHelp(t *testing.T) {
	in := "Intro line.\nSecond line.\n\n#### F-Chat\n**join** <name> - Join a channel.\n**leave** - Leave it.\n\n"
	want := "Intro line.\n\nSecond line.\n\n\n### F-Chat\n\n* **join** <name> – Join a channel.\n\n* **leave** – Leave it."
	if got := spaceHelp(in); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestOrientationFit(t *testing.T) {
	cases := []struct {
		ownOrient, ownGender, otherOrient, otherGender string
		want                                           float64
	}{
		{"Straight", "Male", "Straight", "Female", 1},
		{"Straight", "Male", "Straight", "Male", 0},
		{"Gay", "Male", "Gay", "Male", 1},
		{"Gay", "Female", "Straight", "Male", 0},
		{"Bisexual", "Female", "Bisexual", "Herm", 1},
		{"Straight", "Male", "", "Female", 1},
	}
	for _, tc := range cases {
		got, ok := orientationFit(tc.ownOrient, tc.ownGender, tc.otherOrient, tc.otherGender)
		if !ok || got != tc.want {
			t.Errorf("%+v: got %v (%v), want %v", tc, got, ok, tc.want)
		}
	}
	if _, ok := orientationFit("", "Male", "", "Female"); ok {
		t.Error("unknown orientations shouldn't count")
	}
}

func TestFieldFits(t *testing.T) {
	if s, _ := complementFit("Always dominant", "Always submissive", roleScale); s != 1 {
		t.Errorf("dom/sub should fit perfectly, got %v", s)
	}
	if s, _ := complementFit("Always dominant", "Always dominant", roleScale); s != 0 {
		t.Errorf("two doms shouldn't fit, got %v", s)
	}
	if s, _ := complementFit("Usually Top", "Usually Bottom", positionScale); s != 1 {
		t.Errorf("top/bottom should fit, got %v", s)
	}
	if _, ok := complementFit("", "Switch", roleScale); ok {
		t.Error("missing role shouldn't count")
	}
	if s, _ := furryFit("No furry characters, just humans", "No humans, just furry characters"); s != 0 {
		t.Errorf("opposite furry preferences shouldn't fit, got %v", s)
	}
	if s, _ := furryFit("Furs and / or humans", "Furs and / or humans"); s != 1 {
		t.Errorf("same furry preference should fit, got %v", s)
	}
	if s, _ := languageFit("English", "German"); s != 0 {
		t.Errorf("different languages shouldn't fit, got %v", s)
	}
}

func TestCompareProfilesUsesFields(t *testing.T) {
	kinks := map[string]string{"1": "fave", "2": "yes", "3": "yes", "4": "no"}
	own := &matchProfile{Kinks: kinks, Fields: map[string]string{
		listGender: "Male", listOrientation: "Straight", listRole: "Always dominant", listLanguage: "English",
	}}
	good := &matchProfile{Kinks: kinks, Fields: map[string]string{
		listGender: "Female", listOrientation: "Straight", listRole: "Always submissive", listLanguage: "English",
	}}
	bad := &matchProfile{Kinks: kinks, Fields: map[string]string{
		listGender: "Male", listOrientation: "Straight", listRole: "Always dominant", listLanguage: "German",
	}}
	g, b := compareProfiles(own, good, "", nil), compareProfiles(own, bad, "", nil)
	if g.Score <= b.Score {
		t.Errorf("compatible profile (%v) should score above incompatible one (%v)", g.Score, b.Score)
	}
	if len(g.Factors) != 3 {
		t.Errorf("expected 3 compared fields, got %+v", g.Factors)
	}
}
