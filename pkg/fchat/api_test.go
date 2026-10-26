package fchat

import (
	"context"
	"encoding/json"
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
