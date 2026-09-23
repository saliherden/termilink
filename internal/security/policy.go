package security

import (
	"fmt"
	"path/filepath"
	"regexp"
)

// ApprovalMode controls which chat messages trigger the dangerous-command
// approval gate.
type ApprovalMode string

const (
	ApprovalAll    ApprovalMode = "all"
	ApprovalWorker ApprovalMode = "worker"
	ApprovalOff    ApprovalMode = "off"
)

// builtinDanger are deliberately narrow: only system-level / destructive
// patterns ask for approval, so normal development commands stay friction-free.
var builtinDanger = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-?[a-z]*(?:r[a-z]*f|f[a-z]*r)[a-z]*\s+([~/]|\$HOME)`),
	regexp.MustCompile(`(?i)\bdd\s+if=.*\bof=/dev/\S+`),
	regexp.MustCompile(`(?i)\b(mkfs|fdisk|gdisk|parted)\b`),
	regexp.MustCompile(`(?i)\b(shutdown|reboot|poweroff|halt|sync)\b`),
	regexp.MustCompile(`(?i)(^|[;&|]\s*)sudo\s+`),
	regexp.MustCompile(`:\s*\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;`),
	regexp.MustCompile(`(?i)\bchown\s+[^/|&;]{1,80}/(?:\s|$)|\bchmod\s+(-R\s+)?[0-7]{3,4}\s+/`),
	regexp.MustCompile(`(?i)>+\s*=/dev/\S+|(?i)>>?\s*/dev/\S+`),
}

// Policy bundles the approval gate and workspace scope that the Telegram
// handler enforces for chat commands.
type Policy struct {
	Approval  ApprovalMode
	workspace []string
	extras    []*regexp.Regexp
}

func NewPolicy(mode string, dangerousPatterns, workspace []string) (*Policy, error) {
	if mode == "" {
		mode = string(ApprovalAll)
	}
	p := &Policy{Approval: ApprovalMode(mode), workspace: cleanRoots(workspace)}
	switch p.Approval {
	case ApprovalAll, ApprovalWorker, ApprovalOff:
	default:
		return nil, fmt.Errorf("invalid approval mode %q", mode)
	}
	for _, pat := range dangerousPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid dangerous_pattern %q: %w", pat, err)
		}
		p.extras = append(p.extras, re)
	}
	return p, nil
}

// NeedsApproval reports whether raw requires chat approval for the given user
// role.
func (p *Policy) NeedsApproval(isOwner bool, raw string) bool {
	if p == nil {
		return false
	}
	switch p.Approval {
	case ApprovalOff:
		return false
	case ApprovalWorker:
		if isOwner {
			return false
		}
	}
	return p.DangerMatch(raw)
}

func (p *Policy) DangerMatch(raw string) bool {
	for _, re := range builtinDanger {
		if re.MatchString(raw) {
			return true
		}
	}
	for _, re := range p.extras {
		if re.MatchString(raw) {
			return true
		}
	}
	return false
}

// WorkspaceEmpty reports whether no workspace roots are configured.
func (p *Policy) WorkspaceEmpty() bool {
	return p == nil || len(p.workspace) == 0
}

// InWorkspace checks whether path (already cleaned) lives under one of the
// allowed roots.
func (p *Policy) InWorkspace(path string) bool {
	if p == nil || len(p.workspace) == 0 {
		return true
	}
	for _, root := range p.workspace {
		if under(root, path) {
			return true
		}
	}
	return false
}

func cleanRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, filepath.Clean(r))
	}
	return out
}

func under(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return len(rel) >= 2 && rel[0] != '.' && rel[1] != '.'
}
