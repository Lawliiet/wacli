package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
)

// autoDownloader applies the download rules to live messages and enforces the
// rules file's retention limits on the files it fetched.
type autoDownloader struct {
	app   *App
	src   *downloadRulesSource
	queue *mediaQueue

	evictMu sync.Mutex

	considered atomic.Int64
	enqueued   atomic.Int64
	downloaded atomic.Int64
	evicted    atomic.Int64
}

func (a *App) newAutoDownloader(rulesPath string, queue *mediaQueue) *autoDownloader {
	return &autoDownloader{
		app:   a,
		queue: queue,
		src: newDownloadRulesSource(rulesPath, func(code, msg string) {
			a.emitWarning(code, msg, map[string]any{"path": rulesPath})
		}),
	}
}

func (d *autoDownloader) debugf(event string, data map[string]any, format string, args ...any) {
	if d.app.eventsEnabled() {
		d.app.emitEvent(event, data)
		return
	}
	if os.Getenv("WACLI_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "debug: "+format+"\n", args...)
	}
}

func (d *autoDownloader) listMembership(chat string) ListMembership {
	return func(ref, _ string) (bool, error) {
		l, err := d.app.db.ResolveChatList(ref)
		if err != nil {
			return false, err
		}
		if l.Deleted {
			return false, nil
		}
		return d.app.db.ChatInList(l.ID, chat)
	}
}

// consider decides whether the media of a live message is fetched and, if so,
// queues it. chat is the canonical chat JID the message was stored under.
func (d *autoDownloader) consider(ctx context.Context, pm wa.ParsedMessage, chat string) {
	if d == nil || pm.Media == nil || pm.ID == "" {
		return
	}
	rules := d.src.Current()
	if rules == nil {
		return
	}
	d.considered.Add(1)
	cand := DownloadCandidate{
		Chat:      chat,
		Kind:      ChatKindForJID(pm.Chat),
		MediaType: pm.Media.Type,
		Size:      pm.Media.FileLength,
		Filename:  pm.Media.Filename,
		Mime:      pm.Media.MimeType,
	}
	dec := rules.Decide(cand, d.listMembership(chat))
	d.debugf("media_auto_decision", map[string]any{
		"chat_jid": chat, "msg_id": pm.ID, "media_type": cand.MediaType, "size": cand.Size,
		"download": dec.Download, "rule": dec.Rule, "reason": dec.Reason,
	}, "auto-download %s/%s (%s, %d bytes): download=%v (%s)", chat, pm.ID, cand.MediaType, cand.Size, dec.Download, dec.Reason)
	if !dec.Download || d.queue == nil {
		return
	}
	d.enqueued.Add(1)
	d.queue.enqueue(ctx, mediaJob{chatJID: chat, msgID: pm.ID, auto: true, rule: dec.Rule})
}

// afterDownload runs after each automatic download.
func (d *autoDownloader) afterDownload(job mediaJob) {
	d.downloaded.Add(1)
	d.app.emitEvent("media_auto_downloaded", map[string]any{
		"chat_jid": job.chatJID, "msg_id": job.msgID, "rule": job.rule, "total": d.downloaded.Load(),
	})
	d.evict(nowUTC())
}

// evict removes the oldest automatically downloaded files while the rules'
// limits are exceeded. Only files recorded as automatic are touched; message
// rows and media metadata stay, only local_path/downloaded_at are cleared. Files
// of messages the sender has since deleted are kept and still count toward the
// total, so a deleted message's media outlives the cap.
func (d *autoDownloader) evict(now time.Time) (int, error) {
	rules := d.src.Current()
	if rules == nil || (rules.Limits.TotalBytes <= 0 && rules.Limits.KeepDays <= 0) {
		return 0, nil
	}
	d.evictMu.Lock()
	defer d.evictMu.Unlock()
	files, err := d.app.db.ListAutoDownloads()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, f := range files {
		total += f.SizeBytes
	}
	cutoff := time.Time{}
	if rules.Limits.KeepDays > 0 {
		cutoff = now.AddDate(0, 0, -rules.Limits.KeepDays)
	}
	count := 0
	var firstErr error
	for _, f := range files { // oldest first
		tooOld := !cutoff.IsZero() && f.DownloadedAt.Before(cutoff)
		tooBig := rules.Limits.TotalBytes > 0 && total > rules.Limits.TotalBytes
		if !tooOld && !tooBig {
			continue
		}
		if f.Revoked {
			continue
		}
		if err := d.evictFile(f, now); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		total -= f.SizeBytes
		count++
	}
	if count > 0 {
		d.evicted.Add(int64(count))
		d.app.emitEvent("media_auto_evicted", map[string]any{"files": count, "total": d.evicted.Load()})
	}
	return count, firstErr
}

func (d *autoDownloader) evictFile(f store.AutoDownload, now time.Time) error {
	if err := d.app.removeStoreMediaFile(f.LocalPath); err != nil {
		return err
	}
	return d.app.db.MarkAutoDownloadEvicted(f.ChatJID, f.MsgID, f.LocalPath, now)
}

// removeStoreMediaFile deletes a file only when it lives under the store's
// media directory, so a bad record can never delete anything elsewhere.
func (a *App) removeStoreMediaFile(p string) error {
	root, err := filepath.Abs(filepath.Join(a.opts.StoreDir, "media"))
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to remove %s: outside the store media directory", p)
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// run applies retention at start and then hourly, so age limits take effect
// even when no new media arrives.
func (d *autoDownloader) run(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if _, err := d.evict(nowUTC()); err != nil {
			d.app.emitWarning("media_auto_evict_failed", "automatic media eviction failed: "+err.Error(), map[string]any{"error": err.Error()})
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
