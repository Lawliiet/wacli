package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleChatListEvent mirrors WhatsApp chat lists (app-state labels) into the
// local store. Stored rows carry the action timestamp, so a replayed or
// out-of-order older patch never overwrites a newer one.
func (a *App) handleChatListEvent(ctx context.Context, evt any) error {
	switch v := evt.(type) {
	case *events.LabelEdit:
		if v == nil || strings.TrimSpace(v.LabelID) == "" || v.Action == nil {
			return nil
		}
		act := v.Action
		err := a.db.UpsertChatList(store.ChatList{
			ID:           v.LabelID,
			Name:         act.GetName(),
			Type:         act.GetType().String(),
			Color:        act.GetColor(),
			PredefinedID: act.GetPredefinedID(),
			OrderIndex:   act.GetOrderIndex(),
			Deleted:      act.GetDeleted(),
			IsActive:     act.GetIsActive(),
			IsImmutable:  act.GetIsImmutable(),
			UpdatedAtMS:  store.TimeMillis(v.Timestamp),
		})
		if err != nil {
			a.emitChatListWarning("label_edit", v.LabelID, err)
		}
		return err
	case *events.LabelAssociationChat:
		if v == nil || strings.TrimSpace(v.LabelID) == "" || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := canonicalJIDString(a.canonicalStoreJID(ctx, v.JID))
		err := a.db.SetChatListMember(v.LabelID, chat, v.Action.GetLabeled(), store.TimeMillis(v.Timestamp))
		if err != nil {
			a.emitChatListWarning("label_association", v.LabelID, err)
		}
		return err
	}
	return nil
}

// handleFavoritesEvent stores the favorites action, which whatsmeow only
// surfaces as a generic AppState event. The action lists every favorite, so it
// replaces the membership of the local "favorites" list.
func (a *App) handleFavoritesEvent(ctx context.Context, evt *events.AppState) error {
	if evt == nil || evt.SyncActionValue == nil {
		return nil
	}
	fav := evt.GetFavoritesAction()
	if fav == nil {
		return nil
	}
	jids := make([]string, 0, len(fav.GetFavorites()))
	for _, f := range fav.GetFavorites() {
		parsed, err := types.ParseJID(strings.TrimSpace(f.GetID()))
		if err != nil {
			continue
		}
		jids = append(jids, canonicalJIDString(a.canonicalStoreJID(ctx, parsed)))
	}
	err := a.db.ReplaceChatListMembers(
		store.ChatList{ID: store.FavoritesListID, Name: "Favorites", Type: "favorites"},
		jids, evt.GetTimestamp())
	if err != nil {
		a.emitChatListWarning("favorites", store.FavoritesListID, err)
	}
	return err
}

func (a *App) emitChatListWarning(kind, id string, err error) {
	a.emitWarning(
		"chat_list_store_failed",
		fmt.Sprintf("warning: failed to store %s chat list state for %s: %v", kind, id, err),
		map[string]any{"kind": kind, "list_id": id, "error": err.Error()},
	)
}

// chatListCollections are the app-state collections that carry chat lists:
// label definitions and memberships live in regular, favorites in regular_high.
var chatListCollections = []appstate.WAPatchName{appstate.WAPatchRegular, appstate.WAPatchRegularHigh}

// SyncChatLists asks WhatsApp for a full copy of the collections that carry
// chat lists and stores what arrives. It reuses the replay path that chat-state
// writes already run before a patch (full fetch, missing-key wait, primary
// device recovery on a hash mismatch) and sends nothing to the account itself.
func (a *App) SyncChatLists(ctx context.Context) error {
	release, err := a.acquireChatStateSync(ctx)
	if err != nil {
		return err
	}
	defer release()
	for _, collection := range chatListCollections {
		generation, _, err := a.db.BeginAppStateRecovery(string(collection))
		if err != nil {
			return fmt.Errorf("begin WhatsApp app state recovery for %s: %w", collection, err)
		}
		tracker := &appStatePersistenceTracker{}
		if err := a.replayRequiredAppState(ctx, collection, generation, tracker); err != nil {
			return fmt.Errorf("sync chat lists from %s: %w", collection, err)
		}
	}
	return nil
}
