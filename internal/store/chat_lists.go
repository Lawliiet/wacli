package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// FavoritesListID is the local id of the favorites list. WhatsApp delivers it
// as one action holding every favorite, not as a label, so it has no label id.
const FavoritesListID = "favorites"

// ChatList is one WhatsApp chat list (a label in app-state terms).
type ChatList struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Color        int32  `json:"color"`
	PredefinedID int32  `json:"predefined_id"`
	OrderIndex   int32  `json:"order_index"`
	Deleted      bool   `json:"deleted"`
	IsActive     bool   `json:"is_active"`
	IsImmutable  bool   `json:"is_immutable"`
	// UpdatedAtMS is the app-state action time in unix milliseconds.
	UpdatedAtMS int64 `json:"updated_at_ms"`
}

// ChatListSummary is a list with the number of chats currently in it.
type ChatListSummary struct {
	ChatList
	MemberCount int `json:"member_count"`
}

// ChatListMember is one chat in a list. Name and Kind are empty when the chat
// is not in the local chats table yet.
type ChatListMember struct {
	JID           string    `json:"jid"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	LastMessageTS time.Time `json:"last_message_ts"`
}

// TimeMillis converts an app-state timestamp to the unit used by the chat list
// tables; the zero time maps to 0.
func TimeMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

// UpsertChatList stores a list definition unless the stored one is newer.
func (d *DB) UpsertChatList(l ChatList) error {
	l.ID = strings.TrimSpace(l.ID)
	if l.ID == "" {
		return fmt.Errorf("chat list id is required")
	}
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO chat_lists(label_id, name, type, color, predefined_id, order_index, deleted, is_active, is_immutable, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(label_id) DO UPDATE SET
			name = excluded.name,
			type = excluded.type,
			color = excluded.color,
			predefined_id = excluded.predefined_id,
			order_index = excluded.order_index,
			deleted = excluded.deleted,
			is_active = excluded.is_active,
			is_immutable = excluded.is_immutable,
			updated_at = excluded.updated_at
		WHERE excluded.updated_at >= chat_lists.updated_at
	`, l.ID, l.Name, strings.ToLower(l.Type), l.Color, l.PredefinedID, l.OrderIndex,
		boolToInt(l.Deleted), boolToInt(l.IsActive), boolToInt(l.IsImmutable), l.UpdatedAtMS)
	return err
}

// SetChatListMember records that a chat joined or left a list, unless the
// stored state is newer. The list row is not required to exist yet: membership
// patches can arrive before the list definition.
func (d *DB) SetChatListMember(labelID, chatJID string, labeled bool, updatedAtMS int64) error {
	labelID = strings.TrimSpace(labelID)
	chatJID = strings.TrimSpace(chatJID)
	if labelID == "" || chatJID == "" {
		return fmt.Errorf("list id and chat JID are required")
	}
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO chat_list_members(label_id, chat_jid, labeled, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(label_id, chat_jid) DO UPDATE SET
			labeled = excluded.labeled,
			updated_at = excluded.updated_at
		WHERE excluded.updated_at >= chat_list_members.updated_at
	`, labelID, chatJID, boolToInt(labeled), updatedAtMS)
	return err
}

// ReplaceChatListMembers makes chatJIDs the full membership of a list as of
// updatedAtMS, for sources that deliver the whole set at once (favorites).
// Members that are absent from the set are marked removed, and rows already
// newer than updatedAtMS are left alone. A missing list row is created with
// the given defaults; an existing one is not renamed.
func (d *DB) ReplaceChatListMembers(def ChatList, chatJIDs []string, updatedAtMS int64) error {
	def.ID = strings.TrimSpace(def.ID)
	if def.ID == "" {
		return fmt.Errorf("chat list id is required")
	}
	tx, err := d.sql.BeginTx(storeCtx(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO chat_lists(label_id, name, type, is_active, is_immutable, updated_at)
		VALUES(?, ?, ?, 1, 1, ?)
	`, def.ID, def.Name, strings.ToLower(def.Type), updatedAtMS); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE chat_list_members SET labeled = 0, updated_at = ?
		WHERE label_id = ? AND labeled = 1 AND updated_at <= ?
	`, updatedAtMS, def.ID, updatedAtMS); err != nil {
		return err
	}
	for _, jid := range chatJIDs {
		jid = strings.TrimSpace(jid)
		if jid == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO chat_list_members(label_id, chat_jid, labeled, updated_at)
			VALUES(?, ?, 1, ?)
			ON CONFLICT(label_id, chat_jid) DO UPDATE SET labeled = 1, updated_at = excluded.updated_at
			WHERE excluded.updated_at >= chat_list_members.updated_at
		`, def.ID, jid, updatedAtMS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) chatListTablesExist() (bool, error) {
	for _, t := range []string{"chat_lists", "chat_list_members"} {
		ok, err := d.tableExists(t)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// ListChatLists returns known lists with their current member counts, in
// WhatsApp's own order. Deleted lists are skipped unless includeDeleted is set.
func (d *DB) ListChatLists(includeDeleted bool) ([]ChatListSummary, error) {
	if ok, err := d.chatListTablesExist(); err != nil || !ok {
		return []ChatListSummary{}, err
	}
	q := `
		SELECT l.label_id, l.name, l.type, l.color, l.predefined_id, l.order_index,
			l.deleted, l.is_active, l.is_immutable, l.updated_at,
			(SELECT COUNT(*) FROM chat_list_members m WHERE m.label_id = l.label_id AND m.labeled = 1)
		FROM chat_lists l`
	if !includeDeleted {
		q += ` WHERE l.deleted = 0`
	}
	q += ` ORDER BY l.order_index, l.name COLLATE NOCASE, l.label_id`
	rows, err := d.sql.QueryContext(storeCtx(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatListSummary{}
	for rows.Next() {
		var s ChatListSummary
		var deleted, active, immutable int
		if err := rows.Scan(&s.ID, &s.Name, &s.Type, &s.Color, &s.PredefinedID, &s.OrderIndex,
			&deleted, &active, &immutable, &s.UpdatedAtMS, &s.MemberCount); err != nil {
			return nil, err
		}
		s.Deleted, s.IsActive, s.IsImmutable = deleted != 0, active != 0, immutable != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// ResolveChatList finds one list by exact id or, failing that, by name
// (case-insensitive, deleted lists excluded). A name shared by several lists is
// an error that names every candidate id.
func (d *DB) ResolveChatList(ref string) (ChatList, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ChatList{}, fmt.Errorf("list id or name is required")
	}
	all, err := d.ListChatLists(true)
	if err != nil {
		return ChatList{}, err
	}
	for _, s := range all {
		if s.ID == ref {
			return s.ChatList, nil
		}
	}
	var matches []ChatList
	for _, s := range all {
		if !s.Deleted && strings.EqualFold(s.Name, ref) {
			matches = append(matches, s.ChatList)
		}
	}
	switch len(matches) {
	case 0:
		return ChatList{}, fmt.Errorf("no chat list with id or name %q (run `wacli lists` to see them)", ref)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.ID
	}
	sort.Strings(ids)
	return ChatList{}, fmt.Errorf("list name %q is ambiguous; use one of these ids: %s", ref, strings.Join(ids, ", "))
}

// ChatListMembers returns the chats currently in a list, newest activity first.
func (d *DB) ChatListMembers(labelID string) ([]ChatListMember, error) {
	if ok, err := d.chatListTablesExist(); err != nil || !ok {
		return []ChatListMember{}, err
	}
	rows, err := d.sql.QueryContext(storeCtx(), `
		SELECT m.chat_jid, COALESCE(c.kind,''), COALESCE(c.name,''), COALESCE(c.last_message_ts,0)
		FROM chat_list_members m LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.label_id = ? AND m.labeled = 1
		ORDER BY COALESCE(c.last_message_ts,0) DESC, m.chat_jid
	`, labelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatListMember{}
	for rows.Next() {
		var m ChatListMember
		var ts sql.NullInt64
		if err := rows.Scan(&m.JID, &m.Kind, &m.Name, &ts); err != nil {
			return nil, err
		}
		m.LastMessageTS = fromUnix(ts.Int64)
		out = append(out, m)
	}
	return out, rows.Err()
}
