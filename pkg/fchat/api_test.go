package fchat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestGetFriendsAndBookmarks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/api/friend-bookmark-lists.php" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		if r.PostForm.Get("ticket") != "good" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "Invalid ticket."})
			return
		}
		if !r.PostForm.Has("bookmarklist") || !r.PostForm.Has("friendlist") {
			t.Error("list flags missing from request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bookmarklist": []string{"Bookmarked"},
			"friendlist": []map[string]any{
				{"source": "Test Char", "dest": "Friend A", "last_online": 0},
				{"source": "Other Char", "dest": "Friend B", "last_online": 0},
			},
			"error": "",
		})
	}))
	defer srv.Close()
	api := &API{SiteURL: srv.URL}

	_, err := api.GetFriendsAndBookmarks(context.Background(), "acc", "bad")
	if !IsInvalidTicket(err) {
		t.Fatalf("expected invalid ticket error, got %v", err)
	}
	lists, err := api.GetFriendsAndBookmarks(context.Background(), "acc", "good")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lists.Bookmarks, []string{"Bookmarked"}) {
		t.Errorf("unexpected bookmarks %v", lists.Bookmarks)
	}
	if friends := FriendsOf("test char", lists.Friends); !slices.Equal(friends, []string{"Friend A"}) {
		t.Errorf("unexpected friends %v", friends)
	}
}

func TestTicketFriendsOf(t *testing.T) {
	// getApiTicket.php names the account's character dest_name.
	tr := &TicketResponse{Friends: []Friend{{DestName: "Test Char", SourceName: "Friend A"}}}
	if friends := FriendsOf("Test Char", tr.FriendEntries()); !slices.Equal(friends, []string{"Friend A"}) {
		t.Errorf("unexpected friends %v", friends)
	}
}

func TestCharacterDataInfotags(t *testing.T) {
	var cd CharacterData
	if err := json.Unmarshal([]byte(`{"name":"X","infotags":{"3":"2","1":25,"9":"Wolf"},"error":""}`), &cd); err != nil {
		t.Fatal(err)
	}
	if cd.Name != "X" || cd.Infotags["3"] != "2" || cd.Infotags["1"] != "25" || cd.Infotags["9"] != "Wolf" {
		t.Errorf("unexpected data %+v", cd)
	}
	cd = CharacterData{}
	if err := json.Unmarshal([]byte(`{"name":"Y","infotags":[],"kinks":[],"custom_kinks":[]}`), &cd); err != nil || len(cd.Infotags) != 0 || cd.Name != "Y" {
		t.Errorf("empty infotags: %+v %v", cd, err)
	}
}

func TestCharacterDataDetails(t *testing.T) {
	var cd CharacterData
	err := json.Unmarshal([]byte(`{"name":"X","description":"[b]Hi[/b]","custom_title":"Wolf","views":"1234",
		"created_at":1500000000,"updated_at":"1600000000","kinks":{"12":"fave","13":"no"},
		"custom_kinks":{"5":{"name":"Hugs","description":"","choice":"fave","children":[]}},
		"images":[{"image_id":"1"},{"image_id":"2"}]}`), &cd)
	if err != nil {
		t.Fatal(err)
	}
	if cd.Views != 1234 || cd.CreatedAt != 1500000000 || cd.UpdatedAt != 1600000000 || cd.CustomTitle != "Wolf" {
		t.Errorf("unexpected numbers %+v", cd)
	}
	if cd.Kinks["12"] != "fave" || cd.Kinks["13"] != "no" || len(cd.CustomKinks) != 1 || cd.CustomKinks[0].Name != "Hugs" || cd.Images != 2 {
		t.Errorf("unexpected kinks %+v", cd)
	}
}

func TestFriendRequestsAndMemos(t *testing.T) {
	var gotForm map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = map[string]string{"path": r.URL.Path}
		for key := range r.PostForm {
			gotForm[key] = r.PostForm.Get(key)
		}
		switch r.URL.Path {
		case "/json/api/friend-bookmark-lists.php":
			_, _ = w.Write([]byte(`{"requestlist":[{"id":"12","source":"Them","dest":"Me"}],"requestpending":[{"id":34,"source":"Me","dest":"Other"}],"error":""}`))
		case "/json/api/character-memo-get2.php":
			_, _ = w.Write([]byte(`{"note":null,"id":5}`))
		case "/json/api/request-accept.php":
			_, _ = w.Write([]byte(`{"error":"Friend request does not exist."}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	api := &API{SiteURL: srv.URL}
	ctx := context.Background()

	reqs, err := api.GetFriendRequests(ctx, "acc", "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs.Incoming) != 1 || reqs.Incoming[0].ID != 12 || reqs.Incoming[0].Source != "Them" ||
		len(reqs.Outgoing) != 1 || reqs.Outgoing[0].ID != 34 {
		t.Errorf("unexpected requests %+v", reqs)
	}
	if !(gotForm["requestlist"] != "" && gotForm["requestpending"] != "") {
		t.Errorf("request flags missing: %v", gotForm)
	}

	memo, err := api.GetMemo(ctx, "acc", "t", "Them")
	if err != nil || memo != "" || gotForm["target"] != "Them" {
		t.Errorf("GetMemo = %q, %v (form %v)", memo, err, gotForm)
	}
	if err = api.SaveMemo(ctx, "acc", "t", "Them", "nice"); err != nil || gotForm["target_name"] != "Them" || gotForm["note"] != "nice" {
		t.Errorf("SaveMemo: %v (form %v)", err, gotForm)
	}
	if err = api.SendFriendRequest(ctx, "acc", "t", "Me", "Them"); err != nil || gotForm["path"] != "/json/api/request-send2.php" ||
		gotForm["source"] != "Me" || gotForm["target"] != "Them" || gotForm["ticket"] != "t" {
		t.Errorf("SendFriendRequest: %v (form %v)", err, gotForm)
	}
	var apiErr *APIError
	if err = api.AcceptFriendRequest(ctx, "acc", "t", 12); !errors.As(err, &apiErr) || gotForm["request_id"] != "12" {
		t.Errorf("AcceptFriendRequest: %v (form %v)", err, gotForm)
	}
}
