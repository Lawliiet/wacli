package store

import (
	"time"
)

// AutoDownload is one file fetched by the rule-based automatic downloader.
type AutoDownload struct {
	ChatJID      string
	MsgID        string
	LocalPath    string
	SizeBytes    int64
	Rule         string
	DownloadedAt time.Time
	// Revoked is true when the message was deleted by its sender. Such files
	// are the ones the downloader exists to keep.
	Revoked bool
}

// RecordAutoDownload stores the local path on the message and marks the file as
// automatically downloaded, in one transaction.
func (d *DB) RecordAutoDownload(chatJID, msgID, localPath, rule string, size int64, at time.Time) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE messages SET local_path = ?, downloaded_at = ? WHERE chat_jid = ? AND msg_id = ?`,
		localPath, unix(at), chatJID, msgID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO media_auto_downloads(chat_jid, msg_id, local_path, size_bytes, rule, downloaded_at, evicted_at)
		VALUES(?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			local_path = excluded.local_path, size_bytes = excluded.size_bytes, rule = excluded.rule,
			downloaded_at = excluded.downloaded_at, evicted_at = NULL`,
		chatJID, msgID, localPath, size, rule, unix(at)); err != nil {
		return err
	}
	return tx.Commit()
}

// ListAutoDownloads returns automatic downloads that still have a file on
// record (not evicted), oldest first.
func (d *DB) ListAutoDownloads() ([]AutoDownload, error) {
	rows, err := d.sql.Query(`
		SELECT a.chat_jid, a.msg_id, a.local_path, a.size_bytes, a.rule, a.downloaded_at, COALESCE(m.revoked, 0)
		FROM media_auto_downloads a
		LEFT JOIN messages m ON m.chat_jid = a.chat_jid AND m.msg_id = a.msg_id
		WHERE a.evicted_at IS NULL
		ORDER BY a.downloaded_at ASC, a.chat_jid, a.msg_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutoDownload
	for rows.Next() {
		var a AutoDownload
		var ts, revoked int64
		if err := rows.Scan(&a.ChatJID, &a.MsgID, &a.LocalPath, &a.SizeBytes, &a.Rule, &ts, &revoked); err != nil {
			return nil, err
		}
		a.DownloadedAt = fromUnix(ts)
		a.Revoked = revoked != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkAutoDownloadEvicted records that the file was removed. The message row,
// text and media metadata stay; only the local path is cleared (by path, so a
// chat migration that moved the row cannot leave a dangling reference).
func (d *DB) MarkAutoDownloadEvicted(chatJID, msgID, localPath string, at time.Time) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE messages SET local_path = NULL, downloaded_at = NULL WHERE local_path = ?`, localPath); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM message_local_media_aliases WHERE local_path = ?`, localPath); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE media_auto_downloads SET evicted_at = ? WHERE chat_jid = ? AND msg_id = ?`, unix(at), chatJID, msgID); err != nil {
		return err
	}
	return tx.Commit()
}

// ChatInList reports whether the chat is currently a member of the list.
func (d *DB) ChatInList(labelID, chatJID string) (bool, error) {
	if ok, err := d.chatListTablesExist(); err != nil || !ok {
		return false, err
	}
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM chat_list_members WHERE label_id = ? AND chat_jid = ? AND labeled = 1`, labelID, chatJID).Scan(&n)
	return n > 0, err
}
