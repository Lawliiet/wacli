package store

import (
	"strings"
	"testing"
)

func TestChatListUpsertKeepsNewestDefinition(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertChatList(ChatList{ID: "1", Name: "Clients", Type: "CUSTOM", IsActive: true, UpdatedAtMS: 2000}); err != nil {
		t.Fatal(err)
	}
	// An older patch replayed later must not win.
	if err := db.UpsertChatList(ChatList{ID: "1", Name: "Old name", Type: "CUSTOM", UpdatedAtMS: 1000}); err != nil {
		t.Fatal(err)
	}
	lists, err := db.ListChatLists(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 || lists[0].Name != "Clients" || lists[0].Type != "custom" || !lists[0].IsActive {
		t.Fatalf("lists = %+v", lists)
	}
	if err := db.UpsertChatList(ChatList{ID: "1", Name: "Clients", Deleted: true, UpdatedAtMS: 3000}); err != nil {
		t.Fatal(err)
	}
	if lists, _ = db.ListChatLists(false); len(lists) != 0 {
		t.Fatalf("deleted list shown: %+v", lists)
	}
	if lists, _ = db.ListChatLists(true); len(lists) != 1 || !lists[0].Deleted {
		t.Fatalf("--all lists = %+v", lists)
	}
}

func TestChatListMembershipRespectsTimestamps(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertChatList(ChatList{ID: "1", Name: "Clients", UpdatedAtMS: 1}); err != nil {
		t.Fatal(err)
	}
	// Removal at t=20 arrives before the older add at t=10.
	if err := db.SetChatListMember("1", "a@s.whatsapp.net", false, 20); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatListMember("1", "a@s.whatsapp.net", true, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatListMember("1", "b@s.whatsapp.net", true, 10); err != nil {
		t.Fatal(err)
	}
	members, err := db.ChatListMembers("1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].JID != "b@s.whatsapp.net" {
		t.Fatalf("members = %+v", members)
	}
	lists, _ := db.ListChatLists(false)
	if lists[0].MemberCount != 1 {
		t.Fatalf("member count = %d", lists[0].MemberCount)
	}
	// A newer add brings the chat back.
	if err := db.SetChatListMember("1", "a@s.whatsapp.net", true, 30); err != nil {
		t.Fatal(err)
	}
	if members, _ = db.ChatListMembers("1"); len(members) != 2 {
		t.Fatalf("members = %+v", members)
	}
}

func TestChatListMembersJoinKnownChats(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertChatMetadata("a@s.whatsapp.net", "dm", "Ayse"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatListMember("1", "a@s.whatsapp.net", true, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatListMember("1", "unknown@g.us", true, 1); err != nil {
		t.Fatal(err)
	}
	members, err := db.ChatListMembers("1")
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, err %v", members, err)
	}
	byJID := map[string]ChatListMember{}
	for _, m := range members {
		byJID[m.JID] = m
	}
	if byJID["a@s.whatsapp.net"].Name != "Ayse" || byJID["unknown@g.us"].Name != "" {
		t.Fatalf("members = %+v", byJID)
	}
	chats, err := db.ListChatsFiltered(ChatListFilter{ListID: "1"})
	if err != nil || len(chats) != 1 || chats[0].JID != "a@s.whatsapp.net" {
		t.Fatalf("filtered chats = %+v, err %v", chats, err)
	}
}

func TestResolveChatList(t *testing.T) {
	db := openTestDB(t)
	for _, l := range []ChatList{
		{ID: "1", Name: "Clients", UpdatedAtMS: 1},
		{ID: "2", Name: "clients", UpdatedAtMS: 1},
		{ID: "3", Name: "Family", UpdatedAtMS: 1},
		{ID: "4", Name: "Gone", Deleted: true, UpdatedAtMS: 1},
	} {
		if err := db.UpsertChatList(l); err != nil {
			t.Fatal(err)
		}
	}
	if l, err := db.ResolveChatList("3"); err != nil || l.Name != "Family" {
		t.Fatalf("by id = %+v, %v", l, err)
	}
	if l, err := db.ResolveChatList("FAMILY"); err != nil || l.ID != "3" {
		t.Fatalf("by name = %+v, %v", l, err)
	}
	if _, err := db.ResolveChatList("clients"); err == nil || !strings.Contains(err.Error(), "1, 2") {
		t.Fatalf("ambiguous err = %v", err)
	}
	if _, err := db.ResolveChatList("Gone"); err == nil {
		t.Fatal("deleted list resolved by name")
	}
	if _, err := db.ResolveChatList("missing"); err == nil {
		t.Fatal("missing list resolved")
	}
}

func TestReplaceChatListMembers(t *testing.T) {
	db := openTestDB(t)
	def := ChatList{ID: FavoritesListID, Name: "Favorites", Type: "FAVORITES"}
	if err := db.ReplaceChatListMembers(def, []string{"a@s.whatsapp.net", "b@s.whatsapp.net"}, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceChatListMembers(def, []string{"b@s.whatsapp.net", "c@s.whatsapp.net"}, 20); err != nil {
		t.Fatal(err)
	}
	// A stale replay must change nothing.
	if err := db.ReplaceChatListMembers(def, []string{"a@s.whatsapp.net"}, 5); err != nil {
		t.Fatal(err)
	}
	members, _ := db.ChatListMembers(FavoritesListID)
	got := map[string]bool{}
	for _, m := range members {
		got[m.JID] = true
	}
	if len(got) != 2 || !got["b@s.whatsapp.net"] || !got["c@s.whatsapp.net"] {
		t.Fatalf("members = %v", got)
	}
}

func TestChatListQueriesWithoutTables(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.sql.Exec(`DROP TABLE chat_lists; DROP TABLE chat_list_members`); err != nil {
		t.Fatal(err)
	}
	if lists, err := db.ListChatLists(true); err != nil || len(lists) != 0 {
		t.Fatalf("lists = %v, err %v", lists, err)
	}
	if m, err := db.ChatListMembers("1"); err != nil || len(m) != 0 {
		t.Fatalf("members = %v, err %v", m, err)
	}
}
