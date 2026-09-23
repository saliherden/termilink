package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/version"
)

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	return cfg, nil
}

func masked(cfg *config.Config) *config.Config {
	cp := *cfg
	cp.Telegram = config.TelegramConfig{BotToken: "***redacted***"}
	return &cp
}

func newStatusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show agent runtime status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			fmt.Printf("Version:        %s\n", version.Version)
			fmt.Printf("Config:         %s\n", cfg.Path)
			fmt.Printf("Whitelisted:    %d user(s)\n", len(cfg.Security.AllowedUsers))
			fmt.Printf("Projects:       %d\n", len(cfg.Projects))
			fmt.Printf("Shell:          %s\n", cfg.Terminal.Shell)
			fmt.Printf("Command timeout: %s\n", cfg.Terminal.CommandTimeout.Std())
			return nil
		},
	}
}

func newConfigCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "show resolved configuration (secrets redacted)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			data, err := yaml.Marshal(masked(cfg))
			if err != nil {
				return err
			}
			fmt.Print(string(data))
			return nil
		},
	}
}

func newProjectsCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "list configured projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			if len(cfg.Projects) == 0 {
				fmt.Println("No projects configured.")
				return nil
			}
			names := make([]string, 0, len(cfg.Projects))
			for n := range cfg.Projects {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				p := cfg.Projects[n]
				fmt.Printf("  %-16s %s\n", n, p.Path)
			}
			return nil
		},
	}
}

func newSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "list persisted terminal sessions",
		RunE: func(_ *cobra.Command, _ []string) error {
			path := session.DefaultStateFile()
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No persisted sessions. The agent keeps sessions in memory while running.")
					return nil
				}
				return err
			}
			var list []session.State
			if len(data) == 0 {
				list = []session.State{}
			} else if err := json.Unmarshal(data, &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("No sessions.")
				return nil
			}
			for _, s := range list {
				cwd := s.Cwd
				if cwd == "" {
					cwd = "(home)"
				}
				last := s.LastCmd
				if last == "" {
					last = "(none)"
				}
				fmt.Printf("  %-12s cwd: %-40s last: %s\n", s.ID, cwd, last)
			}
			return nil
		},
	}
}

func newInitCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "scaffold a configuration file",
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, err := os.Stat(*configPath); err == nil {
				return fmt.Errorf("%s already exists", *configPath)
			}
			data, err := os.ReadFile("config.example.yaml")
			if err != nil {
				return fmt.Errorf("read example config: %w", err)
			}
			if err := os.WriteFile(*configPath, data, 0o600); err != nil {
				return err
			}
			fmt.Printf("Created %s.\n", *configPath)
			fmt.Println("Next steps:")
			fmt.Println("  1. Set security.allowed_users to your Telegram user id.")
			fmt.Println("  2. Export TELEGRAM_BOT_TOKEN=... (or set telegram.bot_token directly).")
			fmt.Println("  3. Run: termilink start")
			return nil
		},
	}
}
