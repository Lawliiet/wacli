package app

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func labelEdit(id, name string, typ waSyncAction.LabelEditAction_ListType, deleted bool, ts time.Time) *events.LabelEdit {
	return &events.LabelEdit{
		LabelID:   id,
		Timestamp: ts,
		Action: &waSyncAction.LabelEditAction{
			Name:       proto.String(name),
			Type:       typ.Enum(),
			Deleted:    proto.Bool(deleted),
			OrderIndex: proto.Int32(2),
			IsActive:   proto.Bool(true),
		},
	}
}

func labelAssoc(id, jid string, labeled bool, ts time.Time) *events.LabelAssociationChat {
	return &events.LabelAssociationChat{
		LabelID:   id,
		JID:       types.NewJID(jid, types.DefaultUserServer),
		Timestamp: ts,
		Action:    &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(labeled)},
	}
}

func TestPersistAppStateEventStoresChatLists(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	ctx := context.Background()
	t0 := time.UnixMilli(1_700_000_000_000)

	for _, evt := range []any{
		labelEdit("7", "Clients", waSyncAction.LabelEditAction_CUSTOM, false, t0),
		labelAssoc("7", "111", true, t0),
		labelAssoc("7", "222", true, t0),
		// Removal, then an older replay of the add: the chat must stay out.
		labelAssoc("7", "222", false, t0.Add(time.Minute)),
		labelAssoc("7", "222", true, t0),
		// Older rename replayed after the newer one.
		labelEdit("7", "Renamed", waSyncAction.LabelEditAction_CUSTOM, false, t0.Add(time.Second)),
		labelEdit("7", "Stale", waSyncAction.LabelEditAction_CUSTOM, false, t0),
	} {
		if err := a.persistAppStateEvent(ctx, evt, nil); err != nil {
			t.Fatalf("persist %T: %v", evt, err)
		}
	}
	lists, err := a.db.ListChatLists(false)
	if err != nil || len(lists) != 1 {
		t.Fatalf("lists = %+v, err %v", lists, err)
	}
	got := lists[0]
	if got.Name != "Renamed" || got.Type != "custom" || got.MemberCount != 1 || got.OrderIndex != 2 {
		t.Fatalf("list = %+v", got)
	}
	members, _ := a.db.ChatListMembers("7")
	if len(members) != 1 || members[0].JID != "111@s.whatsapp.net" {
		t.Fatalf("members = %+v", members)
	}

	if err := a.persistAppStateEvent(ctx, labelEdit("7", "Renamed", waSyncAction.LabelEditAction_CUSTOM, true, t0.Add(time.Hour)), nil); err != nil {
		t.Fatal(err)
	}
	if lists, _ = a.db.ListChatLists(false); len(lists) != 0 {
		t.Fatalf("deleted list still shown: %+v", lists)
	}
}

func TestPersistAppStateEventStoresFavorites(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	ctx := context.Background()
	fav := func(ts int64, ids ...string) *events.AppState {
		action := &waSyncAction.FavoritesAction{}
		for _, id := range ids {
			action.Favorites = append(action.Favorites, &waSyncAction.FavoritesAction_Favorite{ID: proto.String(id)})
		}
		return &events.AppState{
			Index:           []string{"favorites"},
			SyncActionValue: &waSyncAction.SyncActionValue{Timestamp: proto.Int64(ts), FavoritesAction: action},
		}
	}
	for _, evt := range []*events.AppState{
		fav(100, "111@s.whatsapp.net", "222@s.whatsapp.net"),
		fav(200, "222@s.whatsapp.net"),
		fav(50, "111@s.whatsapp.net"), // stale
	} {
		if err := a.persistAppStateEvent(ctx, evt, nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := a.db.ResolveChatList("favorites")
	if err != nil || list.Type != "favorites" {
		t.Fatalf("list = %+v, err %v", list, err)
	}
	members, _ := a.db.ChatListMembers("favorites")
	if len(members) != 1 || members[0].JID != "222@s.whatsapp.net" {
		t.Fatalf("members = %+v", members)
	}
	// A non-favorites app-state event is ignored without error.
	other := &events.AppState{SyncActionValue: &waSyncAction.SyncActionValue{}}
	if err := a.persistAppStateEvent(ctx, other, nil); err != nil {
		t.Fatal(err)
	}
}
