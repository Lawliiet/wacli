package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newMediaRulesCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Check and test automatic download rules files (offline)",
	}
	cmd.AddCommand(newMediaRulesCheckCmd(flags))
	cmd.AddCommand(newMediaRulesTestCmd(flags))
	return cmd
}

func newMediaRulesCheckCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "check PATH",
		Short: "Validate a download rules file and print the parsed rules",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rules, err := app.LoadDownloadRulesFile(args[0])
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, rules)
			}
			fmt.Fprintf(os.Stdout, "OK: %d rule(s)\n", len(rules.Rules))
			for i, r := range rules.Rules {
				name := r.Name
				if name == "" {
					name = fmt.Sprintf("#%d", i+1)
				}
				action := "download"
				if r.Download != nil && !*r.Download {
					action = "skip"
				}
				var parts []string
				add := func(k string, v []string) {
					if len(v) > 0 {
						parts = append(parts, k+"="+strings.Join(v, ","))
					}
				}
				add("chat", r.Match.Chat)
				add("kind", r.Match.Kind)
				add("list", r.Match.List)
				add("types", r.Types)
				add("ext", r.Extensions)
				add("mime", r.Mime)
				if r.MaxBytes > 0 {
					parts = append(parts, fmt.Sprintf("max_bytes=%d", r.MaxBytes))
				}
				if len(parts) == 0 {
					parts = append(parts, "any chat, any media")
				}
				fmt.Fprintf(os.Stdout, "%d. %s: %s %s\n", i+1, name, action, strings.Join(parts, " "))
			}
			if rules.Limits.TotalBytes > 0 || rules.Limits.KeepDays > 0 {
				fmt.Fprintf(os.Stdout, "limits: total_bytes=%d keep_days=%d\n", rules.Limits.TotalBytes, rules.Limits.KeepDays)
			}
			return nil
		},
	}
}

func newMediaRulesTestCmd(flags *rootFlags) *cobra.Command {
	var chat, mediaType, ext, mimeType, kind string
	var size int64
	var inLists []string
	cmd := &cobra.Command{
		Use:   "test PATH --chat JID --type TYPE [--size N] [--ext EXT] [--mime MIME] [--in-list LIST]...",
		Short: "Show which rule a media message would match",
		Long: "Runs one imagined message through a rules file and prints the matching rule.\n" +
			"The chat kind comes from the JID unless --kind is given. This command does not\n" +
			"open the store, so list membership is whatever --in-list says (a list id or name).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rules, err := app.LoadDownloadRulesFile(args[0])
			if err != nil {
				return err
			}
			jid, err := types.ParseJID(strings.TrimSpace(chat))
			if err != nil || jid.IsEmpty() {
				return fmt.Errorf("--chat %q is not a JID", chat)
			}
			if kind == "" {
				kind = app.ChatKindForJID(jid)
			}
			cand := app.DownloadCandidate{
				Chat:      jid.String(),
				Kind:      kind,
				MediaType: strings.ToLower(strings.TrimSpace(mediaType)),
				Size:      uint64(max(size, 0)),
				Mime:      mimeType,
			}
			if e := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), "."); e != "" {
				cand.Exts = []string{e}
			}
			lists := func(ref, _ string) (bool, error) {
				for _, l := range inLists {
					if strings.EqualFold(strings.TrimSpace(l), ref) {
						return true, nil
					}
				}
				return false, nil
			}
			dec := rules.Decide(cand, lists)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, dec)
			}
			verdict := "no download"
			if dec.Download {
				verdict = "download"
			}
			fmt.Fprintf(os.Stdout, "%s: %s\n", verdict, dec.Reason)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&mediaType, "type", "", "media type: image, video, gif, audio, sticker, document")
	cmd.Flags().Int64Var(&size, "size", 0, "declared file size in bytes (0 = unknown)")
	cmd.Flags().StringVar(&ext, "ext", "", "file extension, e.g. pdf")
	cmd.Flags().StringVar(&mimeType, "mime", "", "MIME type, e.g. application/pdf")
	cmd.Flags().StringVar(&kind, "kind", "", "chat kind (dm, group, channel, status); default: derived from the JID")
	cmd.Flags().StringArrayVar(&inLists, "in-list", nil, "treat the chat as a member of this list id or name (repeatable)")
	_ = cmd.MarkFlagRequired("chat")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}
