package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/felipeelias/claude-notifier/internal/approver"
	"github.com/felipeelias/claude-notifier/internal/config"
	"github.com/felipeelias/claude-notifier/internal/dispatch"
	"github.com/felipeelias/claude-notifier/internal/notifier"
	"github.com/felipeelias/claude-notifier/internal/ntfyclient"
	"github.com/felipeelias/claude-notifier/internal/settings"
	ucli "github.com/urfave/cli/v2"
)

const (
	configDirPerms  = 0750
	configFilePerms = 0600
)

// New creates the CLI application.
func New(version string, reg *notifier.Registry) *ucli.App {
	return &ucli.App{
		Name:    "claude-notifier",
		Usage:   "Notification dispatcher and remote approver for Claude Code",
		Version: version,
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Path to config file",
				Value:   config.DefaultPath(),
				EnvVars: []string{"CLAUDE_NOTIFIER_CONFIG"},
			},
		},
		Action: func(cmd *ucli.Context) error {
			return sendAction(cmd, reg)
		},
		Commands: []*ucli.Command{
			initCommand(reg),
			testCommand(reg),
			hookCommand(),
			setupCommand(),
			statusCommand(),
			enableCommand(),
			disableCommand(),
			uninstallCommand(),
		},
	}
}

func loadNotifiers(configPath string, reg *notifier.Registry) ([]notifier.Notifier, *config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}

	var notifiers []notifier.Notifier
	for name, primitives := range cfg.Notifiers {
		factory, ok := reg.All()[name]
		if !ok {
			slog.Warn("unknown notifier plugin, skipping", "name", name)

			continue
		}
		for _, prim := range primitives {
			n := factory()
			err := cfg.Decode(prim, n)
			if err != nil {
				return nil, nil, fmt.Errorf("decoding config for %s: %w", name, err)
			}
			notifiers = append(notifiers, n)
		}
	}

	return notifiers, cfg, nil
}

func sendAction(cmd *ucli.Context, reg *notifier.Registry) error {
	const maxInputSize = 1 << 20 // 1 MiB
	var notif notifier.Notification

	err := json.NewDecoder(io.LimitReader(os.Stdin, maxInputSize)).Decode(&notif)
	if err != nil {
		slog.Error("reading notification from stdin", "error", err)

		return nil // don't fail the hook
	}

	err = notif.Validate()
	if err != nil {
		slog.Error("invalid notification", "error", err)

		return nil // don't fail the hook
	}

	configPath := cmd.String("config")
	notifiers, cfg, err := loadNotifiers(configPath, reg)
	if err != nil {
		slog.Error("loading config", "error", err)

		return nil // don't fail the hook
	}

	ctx := cmd.Context
	if cfg.Global.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Global.Timeout)
		defer cancel()
	}

	if errs := dispatch.Send(ctx, notifiers, notif); len(errs) > 0 {
		for _, err := range errs {
			slog.Error("sending notification", "error", err)
		}
	}

	return nil // always succeed
}

func initCommand(reg *notifier.Registry) *ucli.Command {
	return &ucli.Command{
		Name:  "init",
		Usage: "Create default config file",
		Action: func(cmd *ucli.Context) error {
			configPath := cmd.String("config")

			_, err := os.Stat(configPath)
			if err == nil {
				return fmt.Errorf("config already exists at %s", configPath)
			}

			err = os.MkdirAll(filepath.Dir(configPath), configDirPerms)
			if err != nil {
				return fmt.Errorf("creating config directory: %w", err)
			}

			sample := config.SampleConfig(reg)
			err = os.WriteFile(configPath, []byte(sample), configFilePerms)
			if err != nil {
				return fmt.Errorf("writing config: %w", err)
			}

			_, _ = fmt.Fprintf(cmd.App.Writer, "Config created at %s\n", configPath)

			return nil
		},
	}
}

func testCommand(reg *notifier.Registry) *ucli.Command {
	return &ucli.Command{
		Name:  "test",
		Usage: "Send a test notification to all configured notifiers",
		Action: func(cmd *ucli.Context) error {
			configPath := cmd.String("config")
			notifiers, cfg, err := loadNotifiers(configPath, reg)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			if len(notifiers) == 0 {
				return fmt.Errorf("no notifiers configured in %s", configPath)
			}

			notif := notifier.Notification{
				Message: "This is a test notification from claude-notifier",
				Title:   "claude-notifier test",
			}

			ctx := cmd.Context
			if cfg.Global.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, cfg.Global.Timeout)
				defer cancel()
			}

			if errs := dispatch.Send(ctx, notifiers, notif); len(errs) > 0 {
				for _, err := range errs {
					_, _ = fmt.Fprintf(cmd.App.ErrWriter, "error: %s\n", err)
				}

				return errors.New("some notifiers failed")
			}

			_, _ = fmt.Fprintln(cmd.App.Writer, "Test notification sent successfully")

			return nil
		},
	}
}

func hookCommand() *ucli.Command {
	return &ucli.Command{
		Name:   "hook",
		Usage:  "Handle PermissionRequest hook (called by Claude Code)",
		Hidden: true,
		Action: func(cmd *ucli.Context) error {
			configPath := cmd.String("config")
			cfg, err := config.Load(configPath)
			if err != nil {
				slog.Error("loading config for hook", "error", err)
				fmt.Println(string(approver.AskOutput()))
				return nil
			}

			if cfg.Approver.Topic == "" {
				slog.Debug("no approver configured, asking via CLI")
				fmt.Println(string(approver.AskOutput()))
				return nil
			}

			const maxInputSize = 1 << 20
			var req approver.PermissionRequest
			if err := json.NewDecoder(io.LimitReader(os.Stdin, maxInputSize)).Decode(&req); err != nil {
				slog.Error("reading permission request", "error", err)
				fmt.Println(string(approver.AskOutput()))
				return nil
			}

			auth := ntfyclient.AuthConfig{
				Token:    cfg.Approver.Token,
				Username: cfg.Approver.Username,
				Password: cfg.Approver.Password,
			}

			server := cfg.Approver.Server
			if server == "" {
				server = "https://ntfy.sh"
			}

			timeout := cfg.Approver.Timeout
			if timeout == 0 {
				timeout = 120 * time.Second
			}

			approverCfg := approver.ApproverConfig{
				Server:      server,
				Topic:       cfg.Approver.Topic,
				Timeout:     timeout,
				Auth:        auth,
				TitlePrefix: cfg.Approver.TitlePrefix,
			}

			out := approver.ProcessHook(cmd.Context, req, approverCfg)
			fmt.Println(string(out))
			return nil
		},
	}
}

func setupCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "setup",
		Usage: "Configure remote approval with ntfy",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "server",
				Usage: "ntfy server URL",
				Value: "https://ntfy.sh",
			},
			&ucli.DurationFlag{
				Name:  "timeout",
				Usage: "Approval timeout",
				Value: 120 * time.Second,
			},
		},
		Action: func(cmd *ucli.Context) error {
			configPath := cmd.String("config")
			cfg, err := config.Load(configPath)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("loading config: %w", err)
			}

			topic := generateTopic()
			cfg.Approver = config.Approver{
				Server:  cmd.String("server"),
				Topic:   topic,
				Timeout: cmd.Duration("timeout"),
			}

			if err := saveConfig(configPath, cfg); err != nil {
				return err
			}

			binPath, err := executablePath()
			if err != nil {
				return err
			}

			if err := registerHook(binPath); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.App.Writer, "Remote approval configured!\n")
			_, _ = fmt.Fprintf(cmd.App.Writer, "  Topic:   %s\n", topic)
			_, _ = fmt.Fprintf(cmd.App.Writer, "  Server:  %s\n", cfg.Approver.Server)
			_, _ = fmt.Fprintf(cmd.App.Writer, "  Timeout: %s\n", cfg.Approver.Timeout)
			_, _ = fmt.Fprintf(cmd.App.Writer, "\nSubscribe to topic '%s' on your ntfy app to receive approval requests.\n", topic)

			return nil
		},
	}
}

func statusCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "status",
		Usage: "Show remote approval status",
		Action: func(cmd *ucli.Context) error {
			configPath := cmd.String("config")
			cfg, err := config.Load(configPath)
			if err != nil {
				if os.IsNotExist(err) {
					_, _ = fmt.Fprintln(cmd.App.Writer, "No config file found. Run 'claude-notifier setup' to configure.")
					return nil
				}
				return fmt.Errorf("loading config: %w", err)
			}

			if cfg.Approver.Topic == "" {
				_, _ = fmt.Fprintln(cmd.App.Writer, "Remote approval not configured. Run 'claude-notifier setup' to configure.")
				return nil
			}

			_, _ = fmt.Fprintf(cmd.App.Writer, "Remote approval status:\n")
			_, _ = fmt.Fprintf(cmd.App.Writer, "  Topic:   %s\n", cfg.Approver.Topic)
			_, _ = fmt.Fprintf(cmd.App.Writer, "  Server:  %s\n", cfg.Approver.Server)
			_, _ = fmt.Fprintf(cmd.App.Writer, "  Timeout: %s\n", cfg.Approver.Timeout)

			binPath, _ := executablePath()
			settingsPath, _ := settings.DefaultPath()
			s, _ := settings.Load(settingsPath)
			if s != nil && s.IsHookRegistered(binPath) {
				_, _ = fmt.Fprintln(cmd.App.Writer, "  Hook:    registered")
			} else {
				_, _ = fmt.Fprintln(cmd.App.Writer, "  Hook:    not registered")
			}

			if cfg.Approver.Token != "" {
				_, _ = fmt.Fprintln(cmd.App.Writer, "  Auth:    token")
			} else if cfg.Approver.Username != "" {
				_, _ = fmt.Fprintln(cmd.App.Writer, "  Auth:    basic")
			} else {
				_, _ = fmt.Fprintln(cmd.App.Writer, "  Auth:    none")
			}

			return nil
		},
	}
}

func enableCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "enable",
		Usage: "Register the approval hook in Claude Code settings",
		Action: func(cmd *ucli.Context) error {
			binPath, err := executablePath()
			if err != nil {
				return err
			}
			return registerHook(binPath)
		},
	}
}

func disableCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "disable",
		Usage: "Unregister the approval hook from Claude Code settings",
		Action: func(cmd *ucli.Context) error {
			binPath, err := executablePath()
			if err != nil {
				return err
			}
			return unregisterHook(binPath)
		},
	}
}

func uninstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "uninstall",
		Usage: "Remove approval hook and clear approver config",
		Action: func(cmd *ucli.Context) error {
			binPath, err := executablePath()
			if err != nil {
				return err
			}

			if err := unregisterHook(binPath); err != nil {
				return err
			}

			configPath := cmd.String("config")
			cfg, err := config.Load(configPath)
			if err != nil {
				if os.IsNotExist(err) {
					_, _ = fmt.Fprintln(cmd.App.Writer, "No config to clean up.")
					return nil
				}
				return fmt.Errorf("loading config: %w", err)
			}

			cfg.Approver = config.Approver{}
			if err := saveConfig(configPath, cfg); err != nil {
				return err
			}

			_, _ = fmt.Fprintln(cmd.App.Writer, "Remote approval uninstalled.")
			return nil
		},
	}
}

func generateTopic() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "cra-" + hex.EncodeToString(b)
}

func executablePath() (string, error) {
	path, err := exec.LookPath(os.Args[0])
	if err != nil {
		return "", fmt.Errorf("finding executable: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	return abs, nil
}

func registerHook(binPath string) error {
	settingsPath, err := settings.DefaultPath()
	if err != nil {
		return fmt.Errorf("getting settings path: %w", err)
	}

	s, err := settings.Load(settingsPath)
	if err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}

	s.RegisterHook(binPath, binPath)

	if err := s.Save(settingsPath); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	return nil
}

func unregisterHook(binPath string) error {
	settingsPath, err := settings.DefaultPath()
	if err != nil {
		return fmt.Errorf("getting settings path: %w", err)
	}

	s, err := settings.Load(settingsPath)
	if err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}

	s.UnregisterHook(binPath)

	if err := s.Save(settingsPath); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	return nil
}

func saveConfig(path string, cfg *config.Config) error {
	return config.Save(path, cfg)
}
