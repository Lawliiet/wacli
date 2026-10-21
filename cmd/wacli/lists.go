package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newListsCmd(flags *rootFlags) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "lists",
		Short: "Show WhatsApp chat lists (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			lists, err := a.DB().ListChatLists(all)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, lists)
			}
			fullOutput := fullTableOutput(flags.fullOutput)
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "ID\tNAME\tTYPE\tCHATS\tDELETED")
			for _, l := range lists {
				deleted := ""
				if l.Deleted {
					deleted = "deleted"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", l.ID, tableCell(l.Name, 28, fullOutput), l.Type, l.MemberCount, deleted)
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include deleted lists")
	cmd.AddCommand(newListsMembersCmd(flags))
	cmd.AddCommand(newListsSyncCmd(flags))
	return cmd
}

func newListsMembersCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "members LIST",
		Short: "List the chats in one list (id or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			list, err := a.DB().ResolveChatList(args[0])
			if err != nil {
				return err
			}
			members, err := a.DB().ChatListMembers(list.ID)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"list": list, "members": members})
			}
			fullOutput := fullTableOutput(flags.fullOutput)
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "KIND\tNAME\tJID\tLAST")
			for _, m := range members {
				name := m.Name
				if name == "" {
					name = m.JID
				}
				last := ""
				if !m.LastMessageTS.IsZero() {
					last = m.LastMessageTS.Local().Format("2006-01-02 15:04:05")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Kind, tableCell(name, 28, fullOutput), m.JID, last)
			}
			_ = w.Flush()
			return nil
		},
	}
}

// newListsSyncCmd refreshes the local lists from WhatsApp. It connects like the
// chat-state commands but changes nothing on the account, only the local store.
func newListsSyncCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Ask WhatsApp for a fresh copy of the chat lists",
		Long: "Fetches the app-state collections that carry chat lists and stores them locally. " +
			"It needs the store lock, so stop a running `wacli sync --follow` first; that process " +
			"already keeps lists current as changes arrive.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
			if err != nil {
				return err
			}
			defer func() {
				a.WA().Disconnect()
				removePersistenceHandler()
			}()
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := a.SyncChatLists(ctx); err != nil {
				return err
			}
			lists, err := a.DB().ListChatLists(false)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"ok": true, "action": "lists_sync", "lists": len(lists)})
			}
			fmt.Fprintf(os.Stdout, "lists sync: %s lists\n", strconv.Itoa(len(lists)))
			return nil
		},
	}
}
