package connector

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAliasRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	localpart := makeOpenAliasLocalpart(key, openKindChannel, "my character", "Story Driven: LFRP")
	if !regexp.MustCompile(OpenAliasRegex("example.com")).MatchString("#" + localpart + ":example.com") {
		t.Fatalf("alias %q doesn't match the namespace", localpart)
	}
	kind, login, target, ok := parseOpenAliasLocalpart(key, localpart)
	if !ok || kind != openKindChannel || login != "my character" || target != "Story Driven: LFRP" {
		t.Fatalf("unexpected parse result %q %q %q %v", kind, login, target, ok)
	}
	if _, _, _, ok = parseOpenAliasLocalpart([]byte("other key"), localpart); ok {
		t.Error("alias signed with another key was accepted")
	}
	// Change one character of the payload.
	tampered := []byte(localpart)
	i := len(openAliasPrefix) + 2
	if tampered[i] == 'a' {
		tampered[i] = 'b'
	} else {
		tampered[i] = 'a'
	}
	if _, _, _, ok = parseOpenAliasLocalpart(key, string(tampered)); ok {
		t.Error("tampered alias was accepted")
	}
}

func TestEnsureAliasNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registration.yaml")
	orig := `id: fchat
url: http://localhost:29380
as_token: abc
namespaces:
    users:
        - regex: ^@fchat_.*:example\.com$
          exclusive: true
de.sorunome.msc2409.push_ephemeral: true
`
	if err := os.WriteFile(path, []byte(orig), 0600); err != nil {
		t.Fatal(err)
	}
	regex := OpenAliasRegex("example.com")
	changed, err := ensureAliasNamespace(path, regex)
	if err != nil || !changed {
		t.Fatalf("expected change, got %v %v", changed, err)
	}
	data, _ := os.ReadFile(path)
	var reg struct {
		ID         string `yaml:"id"`
		Namespaces struct {
			Users   []map[string]any `yaml:"users"`
			Aliases []struct {
				Regex     string `yaml:"regex"`
				Exclusive bool   `yaml:"exclusive"`
			} `yaml:"aliases"`
		} `yaml:"namespaces"`
	}
	if err = yaml.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.ID != "fchat" || len(reg.Namespaces.Users) != 1 || len(reg.Namespaces.Aliases) != 1 ||
		reg.Namespaces.Aliases[0].Regex != regex || !reg.Namespaces.Aliases[0].Exclusive {
		t.Fatalf("unexpected registration:\n%s", data)
	}
	if !strings.Contains(string(data), "de.sorunome.msc2409.push_ephemeral: true") {
		t.Errorf("unknown field was lost:\n%s", data)
	}
	if changed, err = ensureAliasNamespace(path, regex); err != nil || changed {
		t.Errorf("second call changed the file: %v %v", changed, err)
	}
	if changed, err = ensureAliasNamespace(filepath.Join(t.TempDir(), "missing.yaml"), regex); err != nil || changed {
		t.Errorf("missing file: %v %v", changed, err)
	}
}

func TestGenderColor(t *testing.T) {
	if genderColor("Male-Herm") != "#007fff" || genderColor("Cunt-boy") != "#00cc66" || genderColor("x") != "" {
		t.Error("unexpected gender colors")
	}
}

func TestOpenLinkURL(t *testing.T) {
	if got := openLinkURL("#fchat_open_x:example.com"); got != "https://matrix.to/#/#fchat_open_x:example.com" {
		t.Errorf("got %q", got)
	}
}
