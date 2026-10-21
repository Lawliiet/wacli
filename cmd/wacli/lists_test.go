package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
)

func seedLists(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.UpsertChatList(store.ChatList{ID: "1", Name: "Clients", Type: "custom", UpdatedAtMS: 1}))
	must(db.UpsertChatList(store.ChatList{ID: "2", Name: "Clients", Type: "custom", UpdatedAtMS: 1}))
	must(db.UpsertChatList(store.ChatList{ID: "3", Name: "Old", Type: "custom", Deleted: true, UpdatedAtMS: 1}))
	must(db.UpsertChatMetadata("a@s.whatsapp.net", "dm", "Ayse"))
	must(db.SetChatListMember("1", "a@s.whatsapp.net", true, 1))
	return dir
}

func runListsCmd(t *testing.T, dir string, asJSON bool, args ...string) (string, error) {
	t.Helper()
	cmd := newListsCmd(&rootFlags{storeDir: dir, readOnly: true, asJSON: asJSON})
	cmd.SetArgs(args)
	var err error
	stdout := captureRootStdout(t, func() { err = cmd.Execute() })
	return stdout, err
}

func TestListsCommandJSONAndAll(t *testing.T) {
	dir := seedLists(t)
	for _, tc := range []struct {
		args []string
		want int
	}{{nil, 2}, {[]string{"--all"}, 3}} {
		stdout, err := runListsCmd(t, dir, true, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Success bool
			Data    []store.ChatListSummary
		}
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatal(err)
		}
		if !res.Success || len(res.Data) != tc.want {
			t.Fatalf("args %v: %+v", tc.args, res)
		}
	}
	stdout, _ := runListsCmd(t, dir, true)
	if !strings.Contains(stdout, `"member_count":1`) || !strings.Contains(stdout, `"id":"1"`) {
		t.Fatalf("json shape: %s", stdout)
	}
}

func TestListsMembersCommand(t *testing.T) {
	dir := seedLists(t)
	stdout, err := runListsCmd(t, dir, true, "members", "1")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Data struct {
			List    store.ChatList
			Members []store.ChatListMember
		}
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatal(err)
	}
	if res.Data.List.ID != "1" || len(res.Data.Members) != 1 || res.Data.Members[0].Name != "Ayse" {
		t.Fatalf("members result = %+v", res)
	}
	if _, err := runListsCmd(t, dir, false, "members", "Clients"); err == nil || !strings.Contains(err.Error(), "1, 2") {
		t.Fatalf("ambiguous name err = %v", err)
	}
	if out, err := runListsCmd(t, dir, false, "members", "1"); err != nil || !strings.Contains(out, "Ayse") {
		t.Fatalf("table = %q, err %v", out, err)
	}
}

func TestListsSyncRejectedInReadOnlyMode(t *testing.T) {
	dir := seedLists(t)
	if _, err := runListsCmd(t, dir, false, "sync"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("err = %v", err)
	}
}
