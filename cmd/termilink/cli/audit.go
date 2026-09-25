package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saliherden/termilink/internal/audit"
)

func newAuditCmd(configPath *string) *cobra.Command {
	var limit int
	var raw bool

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "show recent audit log entries",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			path := cfg.Security.AuditLog
			if path == "" {
				path = audit.DefaultPath()
			}
			if path == "" {
				return fmt.Errorf("could not resolve an audit log path")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Printf("No audit log yet at %s.\n", path)
					return nil
				}
				return err
			}
			lines := nonEmptyLines(data)
			if len(lines) == 0 {
				fmt.Printf("Audit log at %s is empty.\n", path)
				return nil
			}

			tail := lines
			if len(tail) > limit {
				tail = lines[len(tail)-limit:]
			}

			if raw {
				for _, l := range tail {
					fmt.Println(l)
				}
				return nil
			}
			for _, l := range tail {
				var e audit.Entry
				if err := json.Unmarshal([]byte(l), &e); err != nil {
					continue
				}
				fmt.Println(formatEntry(e))
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "n", "n", 20, "number of recent entries to show")
	cmd.Flags().BoolVar(&raw, "json", false, "print raw JSON lines")
	return cmd
}

func nonEmptyLines(data []byte) []string {
	raw := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	lines := make([]string, 0, len(raw))
	for _, b := range raw {
		if len(b) > 0 {
			lines = append(lines, string(b))
		}
	}
	return lines
}

func formatEntry(e audit.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-20s", e.Time.Local().Format("2006-01-02 15:04:05"), e.Action)
	if e.UserID != 0 {
		role := "worker"
		if e.Owner {
			role = "owner"
		}
		fmt.Fprintf(&b, " user=%d(%s)", e.UserID, role)
	}
	if e.Cmd != "" {
		fmt.Fprintf(&b, " cmd: %s", e.Cmd)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " detail: %s", e.Detail)
	}
	if e.OK != nil {
		fmt.Fprintf(&b, " ok=%t", *e.OK)
	}
	if e.DurMS > 0 {
		fmt.Fprintf(&b, " dur=%dms", e.DurMS)
	}
	if e.Err != "" {
		fmt.Fprintf(&b, " err=%s", e.Err)
	}
	return b.String()
}
