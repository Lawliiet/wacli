package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func mustRules(t *testing.T, js string) *DownloadRules {
	t.Helper()
	r, err := ParseDownloadRules([]byte(js))
	if err != nil {
		t.Fatalf("ParseDownloadRules: %v\n%s", err, js)
	}
	return r
}

func TestParseDownloadRulesValidation(t *testing.T) {
	r := mustRules(t, `{"version":1,"rules":[{"name":"x","match":{"chat":"123@g.us","kind":["Group"]},"types":["IMAGE"],"extensions":[".PDF"],"mime":"Video/*","max_bytes":5}],"limits":{"total_bytes":9,"keep_days":2}}`)
	rule := r.Rules[0]
	if rule.Types[0] != "image" || rule.Extensions[0] != "pdf" || rule.Mime[0] != "video/*" || rule.Match.Kind[0] != "group" || rule.Match.Chat[0] != "123@g.us" {
		t.Fatalf("not normalized: %+v", rule)
	}
	bad := map[string]string{
		"version":       `{"version":2,"rules":[]}`,
		"no version":    `{"rules":[]}`,
		"unknown key":   `{"version":1,"rules":[{"match":{},"tpyes":["image"]}]}`,
		"bad type":      `{"version":1,"rules":[{"match":{},"types":["voice"]}]}`,
		"bad kind":      `{"version":1,"rules":[{"match":{"kind":"room"}}]}`,
		"bad chat":      `{"version":1,"rules":[{"match":{"chat":"nope"}}]}`,
		"bad glob":      `{"version":1,"rules":[{"match":{},"mime":"[a"}]}`,
		"neg max":       `{"version":1,"rules":[{"match":{},"max_bytes":-1}]}`,
		"neg limit":     `{"version":1,"rules":[],"limits":{"total_bytes":-1}}`,
		"trailing":      `{"version":1,"rules":[]} {}`,
		"not json":      `rules!`,
		"empty ext":     `{"version":1,"rules":[{"match":{},"extensions":["."]}]}`,
		"list not text": `{"version":1,"rules":[{"match":{"list":5}}]}`,
	}
	for name, js := range bad {
		if _, err := ParseDownloadRules([]byte(js)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func inLists(refs ...string) ListMembership {
	return func(ref, _ string) (bool, error) {
		for _, r := range refs {
			if r == ref {
				return true, nil
			}
		}
		return false, nil
	}
}

func TestDecideMatching(t *testing.T) {
	r := mustRules(t, `{"version":1,"rules":[
		{"name":"no big","match":{"kind":"dm"},"types":["video"],"max_bytes":100,"download":false},
		{"name":"dm-docs","match":{"kind":"dm"},"types":["image","document"],"extensions":["pdf","zip"],"max_bytes":1000},
		{"name":"fam","match":{"list":"Family"},"types":["image"]},
		{"name":"mimes","match":{"chat":["9@g.us","8@g.us"]},"mime":["video/*","application/pdf"]},
		{"name":"no groups","match":{"kind":"group"},"download":false},
		{"name":"all","match":{}}
	]}`)
	dm := "1@s.whatsapp.net"
	cases := []struct {
		name  string
		c     DownloadCandidate
		lists ListMembership
		want  string // rule name or "" for none
		dl    bool
	}{
		{"dm pdf", DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "document", Filename: "a.PDF", Size: 10}, nil, "dm-docs", true},
		{"dm doc wrong ext falls through to all", DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "document", Filename: "a.txt"}, nil, "all", true},
		{"dm too big falls through", DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "image", Filename: "a.pdf", Size: 5000}, nil, "all", true},
		{"dm small video excluded (first wins)", DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "video", Size: 50}, nil, "no big", false},
		{"dm large video falls through", DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "video", Size: 500}, nil, "all", true},
		{"list match", DownloadCandidate{Chat: "2@g.us", Kind: "group", MediaType: "image"}, inLists("Family"), "fam", true},
		{"list miss, group excluded", DownloadCandidate{Chat: "2@g.us", Kind: "group", MediaType: "image"}, inLists("Other"), "no groups", false},
		{"chat + mime glob", DownloadCandidate{Chat: "9@g.us", Kind: "group", MediaType: "video", Mime: "video/mp4"}, nil, "mimes", true},
		{"mime with params", DownloadCandidate{Chat: "8@g.us", Kind: "group", MediaType: "document", Mime: "application/pdf; x=y"}, nil, "mimes", true},
		{"mime miss", DownloadCandidate{Chat: "9@g.us", Kind: "group", MediaType: "image", Mime: "image/png"}, nil, "no groups", false},
		{"channel hits all", DownloadCandidate{Chat: "c@newsletter", Kind: "channel", MediaType: "image"}, nil, "all", true},
		{"extension from mime when no filename", DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "document", Mime: "application/pdf"}, nil, "dm-docs", true},
	}
	for _, tc := range cases {
		got := r.Decide(tc.c, tc.lists)
		if got.Rule != tc.want || got.Download != tc.dl {
			t.Errorf("%s: got %+v, want rule %q download %v", tc.name, got, tc.want, tc.dl)
		}
	}

	// Status never matches an empty match, only an explicit kind or chat.
	st := DownloadCandidate{Chat: "status@broadcast", Kind: "status", MediaType: "image"}
	if d := r.Decide(st, nil); d.Download {
		t.Fatalf("status matched by default: %+v", d)
	}
	r2 := mustRules(t, `{"version":1,"rules":[{"name":"s","match":{"kind":"status"}}]}`)
	if d := r2.Decide(st, nil); !d.Download {
		t.Fatalf("explicit status rule should match: %+v", d)
	}
	// No rules, no download.
	if d := mustRules(t, `{"version":1,"rules":[]}`).Decide(DownloadCandidate{Chat: dm, Kind: "dm", MediaType: "image"}, nil); d.Download || d.Index != 0 {
		t.Fatalf("empty rules: %+v", d)
	}
	// Unresolvable list never matches.
	bad := func(string, string) (bool, error) { return false, os.ErrNotExist }
	if d := mustRules(t, `{"version":1,"rules":[{"match":{"list":"Nope"}}]}`).Decide(st, bad); d.Download {
		t.Fatal("list error must not match")
	}
}

func TestDecideListByNameAndIDThroughStore(t *testing.T) {
	a := newTestApp(t)
	chat := "5@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Bob", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertChatList(store.ChatList{ID: "7", Name: "Family", IsActive: true, UpdatedAtMS: 1}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatListMember("7", chat, true, 1); err != nil {
		t.Fatal(err)
	}
	ad := a.newAutoDownloader(filepath.Join(t.TempDir(), "r.json"), nil)
	cand := DownloadCandidate{Chat: chat, Kind: "dm", MediaType: "image"}
	for _, ref := range []string{"Family", "family", "7"} {
		r := mustRules(t, `{"version":1,"rules":[{"match":{"list":"`+ref+`"}}]}`)
		if !r.Decide(cand, ad.listMembership(chat)).Download {
			t.Errorf("list %q should match", ref)
		}
	}
	// Rename: the id keeps resolving, the old name stops.
	if err := a.db.UpsertChatList(store.ChatList{ID: "7", Name: "Relatives", IsActive: true, UpdatedAtMS: 2}); err != nil {
		t.Fatal(err)
	}
	if !mustRules(t, `{"version":1,"rules":[{"match":{"list":"7"}}]}`).Decide(cand, ad.listMembership(chat)).Download {
		t.Error("id should survive a rename")
	}
	if mustRules(t, `{"version":1,"rules":[{"match":{"list":"Family"}}]}`).Decide(cand, ad.listMembership(chat)).Download {
		t.Error("old name should stop matching")
	}
	// Removed from the list.
	if err := a.db.SetChatListMember("7", chat, false, 3); err != nil {
		t.Fatal(err)
	}
	if mustRules(t, `{"version":1,"rules":[{"match":{"list":"7"}}]}`).Decide(cand, ad.listMembership(chat)).Download {
		t.Error("removed member should not match")
	}
}

func TestDownloadRulesHotReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rules.json")
	var warns []string
	src := newDownloadRulesSource(p, func(code, _ string) { warns = append(warns, code) })

	if src.Current() != nil {
		t.Fatal("missing file should give no rules")
	}
	write := func(s string, mt time.Time) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour)
	write(`{"version":1,"rules":[{"name":"one","match":{}}]}`, t0)
	if r := src.Current(); r == nil || r.Rules[0].Name != "one" {
		t.Fatalf("first load: %+v", r)
	}
	write(`{"version":1,"rules":[{"name":"two","match":{}}]}`, t0.Add(time.Minute))
	if r := src.Current(); r == nil || r.Rules[0].Name != "two" {
		t.Fatalf("reload: %+v", r)
	}
	n := len(warns)
	write(`{ broken`, t0.Add(2*time.Minute))
	if r := src.Current(); r == nil || r.Rules[0].Name != "two" {
		t.Fatalf("invalid file must keep last good rules: %+v", r)
	}
	src.Current()
	if len(warns) != n+1 || warns[len(warns)-1] != "download_rules_invalid" {
		t.Fatalf("expected exactly one invalid warning, got %v", warns)
	}
	write(`{"version":1,"rules":[{"name":"three","match":{}}]}`, t0.Add(3*time.Minute))
	if r := src.Current(); r == nil || r.Rules[0].Name != "three" {
		t.Fatalf("recovery: %+v", r)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if src.Current() != nil {
		t.Fatal("deleted file should stop auto-download")
	}
}

func seedAutoFile(t *testing.T, a *App, chat, id string, size int, at time.Time) string {
	t.Helper()
	if err := a.db.UpsertChat(chat, "dm", "Bob", at); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: id, Timestamp: at, MediaType: "image", Filename: id + ".jpg", DirectPath: "/d", MediaKey: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.opts.StoreDir, "media", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jpg")
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.db.RecordAutoDownload(chat, id, p, "r", int64(size), at); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func newEvictionDownloader(t *testing.T, a *App, limits string) *autoDownloader {
	t.Helper()
	rp := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(rp, []byte(`{"version":1,"rules":[],"limits":`+limits+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return a.newAutoDownloader(rp, nil)
}

func TestEvictionBySizeKeepsManualAndRevoked(t *testing.T) {
	a := newTestApp(t)
	chat := "5@s.whatsapp.net"
	now := time.Now().UTC()
	old := seedAutoFile(t, a, chat, "old", 100, now.Add(-3*time.Hour))
	mid := seedAutoFile(t, a, chat, "mid", 100, now.Add(-2*time.Hour))
	rev := seedAutoFile(t, a, chat, "rev", 100, now.Add(-90*time.Minute))
	if err := a.db.MarkMessageRevoked(chat, "rev"); err != nil {
		t.Fatal(err)
	}
	newer := seedAutoFile(t, a, chat, "new", 100, now.Add(-time.Hour))
	// A manual download of the same kind of file, and a manual re-download of "mid".
	manual := filepath.Join(a.opts.StoreDir, "media", "manual.jpg")
	os.WriteFile(manual, make([]byte, 1000), 0o600)
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "man", Timestamp: now, MediaType: "image"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(chat, "man", manual, now); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(chat, "mid", mid, now); err != nil {
		t.Fatal(err)
	}

	ad := newEvictionDownloader(t, a, `{"total_bytes":250}`)
	n, err := ad.evict(now)
	if err != nil {
		t.Fatal(err)
	}
	// 4 files = 400 bytes; "mid" is manual now so it is no longer auto: 300 > 250.
	// Oldest unprotected auto file is "old"; then 200 <= 250 stops.
	if n != 1 || exists(old) {
		t.Fatalf("evicted %d, old exists=%v", n, exists(old))
	}
	for name, p := range map[string]string{"mid": mid, "rev": rev, "new": newer, "manual": manual} {
		if !exists(p) {
			t.Errorf("%s should still exist", name)
		}
	}
	m, err := a.db.GetMessage(chat, "old")
	if err != nil || m.MediaType != "image" || m.LocalPath != "" {
		t.Fatalf("old row: %+v err=%v (metadata stays, local_path cleared)", m, err)
	}
	if m, _ := a.db.GetMessage(chat, "mid"); m.LocalPath != mid {
		t.Errorf("manual mid lost its path: %q", m.LocalPath)
	}
	// Even with a zero-ish cap only unrevoked auto files go.
	ad2 := newEvictionDownloader(t, a, `{"total_bytes":1}`)
	if _, err := ad2.evict(now); err != nil {
		t.Fatal(err)
	}
	if exists(newer) || !exists(rev) || !exists(mid) || !exists(manual) {
		t.Fatalf("new=%v rev=%v mid=%v manual=%v", exists(newer), exists(rev), exists(mid), exists(manual))
	}
}

func TestEvictionByAge(t *testing.T) {
	a := newTestApp(t)
	chat := "5@s.whatsapp.net"
	now := time.Now().UTC()
	old := seedAutoFile(t, a, chat, "o", 10, now.AddDate(0, 0, -40))
	fresh := seedAutoFile(t, a, chat, "f", 10, now.AddDate(0, 0, -5))
	ad := newEvictionDownloader(t, a, `{"keep_days":30}`)
	if n, err := ad.evict(now); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if exists(old) || !exists(fresh) {
		t.Fatalf("old=%v fresh=%v", exists(old), exists(fresh))
	}
	// Second pass is a no-op.
	if n, _ := ad.evict(now); n != 0 {
		t.Fatalf("second pass evicted %d", n)
	}
}

func TestEvictionRefusesPathsOutsideStore(t *testing.T) {
	a := newTestApp(t)
	outside := filepath.Join(t.TempDir(), "keep.jpg")
	os.WriteFile(outside, []byte("x"), 0o600)
	chat := "5@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Bob", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "x", Timestamp: time.Now(), MediaType: "image"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.RecordAutoDownload(chat, "x", outside, "r", 5, time.Now().AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	ad := newEvictionDownloader(t, a, `{"keep_days":1}`)
	if _, err := ad.evict(time.Now()); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v", err)
	}
	if !exists(outside) {
		t.Fatal("file outside the store was removed")
	}
}

func liveImageEvent(chat types.JID, id string, size uint64, mime string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: time.Now()},
		Message: &waProto.Message{ImageMessage: &waProto.ImageMessage{
			Mimetype: proto.String(mime), DirectPath: proto.String("/d/" + id), MediaKey: []byte{1, 2, 3},
			FileSHA256: []byte{4}, FileEncSHA256: []byte{5}, FileLength: proto.Uint64(size),
		}},
	}
}

func TestLiveMessageEnqueuesByRulesAndDownloadsAutomatically(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	rp := filepath.Join(t.TempDir(), "rules.json")
	rules := `{"version":1,"rules":[
		{"name":"small dm images","match":{"kind":"dm"},"types":["image"],"max_bytes":1000},
		{"name":"no groups","match":{"kind":"group"},"download":false}
	]}`
	if err := os.WriteFile(rp, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	q := newMediaQueue(8)
	ad := a.newAutoDownloader(rp, q)
	a.autoDL.Store(ad)
	t.Cleanup(func() { a.autoDL.Store(nil) })

	dm := types.JID{User: "15555550123", Server: types.DefaultUserServer}
	group := types.JID{User: "120363000000000001", Server: types.GroupServer}
	var stored atomic.Int64
	ctx := context.Background()
	a.handleLiveSyncMessage(ctx, SyncOptions{DownloadRules: rp}, liveImageEvent(dm, "BIG", 5000, "image/jpeg"), &stored, func(string, string) { t.Error("legacy enqueue must not be used") }, nil)
	a.handleLiveSyncMessage(ctx, SyncOptions{DownloadRules: rp}, liveImageEvent(group, "GRP", 10, "image/jpeg"), &stored, func(string, string) {}, nil)
	a.handleLiveSyncMessage(ctx, SyncOptions{DownloadRules: rp}, liveImageEvent(dm, "OK", 500, "image/jpeg"), &stored, func(string, string) {}, nil)

	select {
	case job := <-q.jobs:
		if job.msgID != "OK" || !job.auto || job.rule != "small dm images" {
			t.Fatalf("job = %+v", job)
		}
		if _, err := a.downloadMediaJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		q.done()
	default:
		t.Fatal("expected a queued job")
	}
	select {
	case job := <-q.jobs:
		t.Fatalf("unexpected extra job %+v", job)
	default:
	}
	autos, err := a.db.ListAutoDownloads()
	if err != nil || len(autos) != 1 || autos[0].MsgID != "OK" || autos[0].Rule != "small dm images" || !exists(autos[0].LocalPath) {
		t.Fatalf("auto downloads = %+v err=%v", autos, err)
	}
	if ad.considered.Load() != 3 || ad.enqueued.Load() != 1 || ad.downloaded.Load() != 1 {
		t.Fatalf("counts considered=%d enqueued=%d downloaded=%d", ad.considered.Load(), ad.enqueued.Load(), ad.downloaded.Load())
	}
	// A later manual download of the same message makes it manual: eviction leaves it.
	if err := a.db.MarkMediaDownloaded(autos[0].ChatJID, "OK", autos[0].LocalPath, time.Now()); err != nil {
		t.Fatal(err)
	}
	if autos, _ := a.db.ListAutoDownloads(); len(autos) != 0 {
		t.Fatalf("manual download should clear the auto record: %+v", autos)
	}
}

func TestLiveMessageWithoutRulesFileDownloadsNothing(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	q := newMediaQueue(8)
	a.autoDL.Store(a.newAutoDownloader(filepath.Join(t.TempDir(), "absent.json"), q))
	t.Cleanup(func() { a.autoDL.Store(nil) })
	dm := types.JID{User: "15555550123", Server: types.DefaultUserServer}
	var stored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, liveImageEvent(dm, "X", 1, "image/jpeg"), &stored, func(string, string) {}, nil)
	select {
	case job := <-q.jobs:
		t.Fatalf("unexpected job %+v", job)
	default:
	}
}
