package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	appPkg "github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newSyncCmd(flags *rootFlags) *cobra.Command {
	var once bool
	var follow bool
	var idleExit time.Duration
	var maxReconnect time.Duration
	var staleThreshold time.Duration
	var presenceModeFlag string
	var downloadMedia bool
	var refreshContacts bool
	var refreshGroups bool
	var refreshChannels bool
	var webhookURL string
	var webhookSecret string
	var webhookSecretFile string
	var webhookAllowPrivate bool
	var webhookEventsFlag string
	var sendSpacingFlag string
	var storage syncStorageLimitFlags

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync messages (requires prior auth; never shows QR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			storage.maxMessagesSet = cmd.Flags().Changed("max-messages")
			maxMessages, maxDBSize, err := resolveSyncStorageLimits(storage)
			if err != nil {
				return err
			}
			if webhookSecret != "" && webhookSecretFile != "" {
				return fmt.Errorf("--webhook-secret and --webhook-secret-file are mutually exclusive")
			}
			if webhookSecret != "" && webhookURL == "" {
				return fmt.Errorf("--webhook-secret requires --webhook")
			}
			if webhookSecretFile != "" && webhookURL == "" {
				return fmt.Errorf("--webhook-secret-file requires --webhook")
			}
			if webhookSecretFile != "" {
				webhookSecret, err = readWebhookSecretFile(webhookSecretFile)
				if err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("webhook-events") && webhookURL == "" {
				return fmt.Errorf("--webhook-events requires --webhook")
			}
			webhookEvents, err := appPkg.ParseSyncWebhookEvents(webhookEventsFlag)
			if err != nil {
				return err
			}
			if staleThreshold != 0 && staleThreshold < time.Second {
				return fmt.Errorf("--stale-threshold must be at least 1s, got %s", staleThreshold)
			}
			sendSpacing, err := parseSendSpacing(sendSpacingFlag)
			if err != nil {
				return err
			}
			if maxStaleThreshold := appPkg.MaxStaleThreshold(); staleThreshold >= maxStaleThreshold {
				return fmt.Errorf("--stale-threshold must be less than %s because whatsmeow auto-reconnects after that much keepalive failure, got %s", maxStaleThreshold, staleThreshold)
			}
			presenceMode, err := appPkg.ParseSyncPresenceMode(presenceModeFlag)
			if err != nil {
				return err
			}
			ctx, stop := signalContextWithEvents(out.NewEventWriter(os.Stderr, flags.events))
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}

			mode := appPkg.SyncModeFollow
			if once {
				mode = appPkg.SyncModeOnce
			} else if follow {
				mode = appPkg.SyncModeFollow
			} else {
				mode = appPkg.SyncModeOnce
			}

			var stopSendDelegate func()
			defer func() {
				if stopSendDelegate != nil {
					stopSendDelegate()
				}
			}()
			var afterConnect func(context.Context) error
			if mode == appPkg.SyncModeFollow {
				afterConnect = func(ctx context.Context) error {
					stop, err := startSendDelegateServer(ctx, a, sendSpacing)
					if err != nil {
						return err
					}
					stopSendDelegate = stop
					return nil
				}
			}

			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:                mode,
				PresenceMode:        presenceMode,
				AllowQR:             false,
				AfterConnect:        afterConnect,
				DownloadMedia:       downloadMedia,
				RefreshContacts:     refreshContacts,
				RefreshGroups:       refreshGroups,
				RefreshChannels:     refreshChannels,
				IdleExit:            idleExit,
				MaxReconnect:        maxReconnect,
				StaleThreshold:      staleThreshold,
				MaxMessages:         maxMessages,
				MaxDBSizeBytes:      maxDBSize,
				WarnNoLimits:        true,
				WebhookURL:          webhookURL,
				WebhookSecret:       webhookSecret,
				WebhookAllowPrivate: webhookAllowPrivate,
				WebhookEvents:       webhookEvents,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"synced":          true,
					"messages_stored": res.MessagesStored,
				})
			}
			fmt.Fprintf(os.Stdout, "Messages stored: %d\n", res.MessagesStored)
			return nil
		},
	}

	cmd.Flags().BoolVar(&once, "once", false, "sync until idle and exit")
	cmd.Flags().BoolVar(&follow, "follow", true, "keep syncing until Ctrl+C")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 30*time.Second, "exit after being idle (once mode)")
	cmd.Flags().DurationVar(&maxReconnect, "max-reconnect", 5*time.Minute, "give up reconnecting after this duration (0 = unlimited)")
	cmd.Flags().DurationVar(&staleThreshold, "stale-threshold", 0, "force reconnect when keepalive failures last this long in follow mode (1s-<2m20s, 0 = disabled)")
	cmd.Flags().StringVar(&presenceModeFlag, "presence-mode", string(appPkg.SyncPresenceModeNormal), "global sync presence behavior: normal or quiet")
	cmd.Flags().StringVar(&sendSpacingFlag, "send-spacing", "", "pace delegated sends in follow mode by a fixed duration or random min-max range (e.g. 2s or 500ms-5s; default: disabled)")
	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")
	cmd.Flags().BoolVar(&refreshContacts, "refresh-contacts", false, "refresh contacts from session store into local DB")
	cmd.Flags().BoolVar(&refreshGroups, "refresh-groups", false, "refresh joined groups and participant snapshots (live)")
	cmd.Flags().BoolVar(&refreshChannels, "refresh-channels", false, "refresh subscribed channels (live) into local DB")
	cmd.Flags().StringVar(&webhookURL, "webhook", "", "URL to POST live message JSON")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "HMAC-SHA256 secret for X-Wacli-Signature header")
	cmd.Flags().StringVar(&webhookSecretFile, "webhook-secret-file", "", "read the webhook HMAC secret from a file (must not be group/other accessible); keeps it out of the process list")
	cmd.Flags().BoolVar(&webhookAllowPrivate, "webhook-allow-private", false, "allow webhook URLs that resolve to localhost or private networks")
	cmd.Flags().StringVar(&webhookEventsFlag, "webhook-events", string(appPkg.SyncWebhookEventMessage), "comma-separated event types to POST: message, receipt, chat_presence")
	cmd.Flags().Int64Var(&storage.maxMessages, "max-messages", 0, "maximum total messages to keep in the local DB before sync stops (0 = unlimited, or WACLI_SYNC_MAX_MESSAGES)")
	cmd.Flags().StringVar(&storage.maxDBSize, "max-db-size", "", "maximum wacli.db disk usage before sync stops, e.g. 500MB or 2GB (default: WACLI_SYNC_MAX_DB_SIZE or unlimited)")
	return cmd
}

// readWebhookSecretFile returns the trimmed content of path. The secret is never
// included in errors.
func readWebhookSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("--webhook-secret-file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("--webhook-secret-file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("--webhook-secret-file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("--webhook-secret-file %s is accessible by group or others (mode %04o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("--webhook-secret-file: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("--webhook-secret-file %s is empty", path)
	}
	return secret, nil
}
