package security

import "testing"

func TestNewPolicyDefaults(t *testing.T) {
	p, err := NewPolicy("", nil, nil)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	if p.Approval != ApprovalAll {
		t.Fatalf("default mode = %q, want all", p.Approval)
	}
}

func TestNewPolicyInvalidMode(t *testing.T) {
	if _, err := NewPolicy("maybe", nil, nil); err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if _, err := NewPolicy("all", []string{"[unclosed"}, nil); err == nil {
		t.Fatal("expected error for invalid extra pattern")
	}
}

func TestNeedsApprovalModes(t *testing.T) {
	raw := "rm -rf /tmp/backup"
	p, _ := NewPolicy("", nil, nil)
	if !p.NeedsApproval(true, raw) {
		t.Fatal("all mode must gate owner too")
	}
	p2, _ := NewPolicy("worker", nil, nil)
	if p2.NeedsApproval(true, raw) {
		t.Fatal("worker mode must skip owner")
	}
	if !p2.NeedsApproval(false, raw) {
		t.Fatal("worker mode must gate workers")
	}
	p3, _ := NewPolicy("off", nil, nil)
	if p3.NeedsApproval(false, raw) {
		t.Fatal("off mode must never gate")
	}
}

func TestBuiltinDangerMatches(t *testing.T) {
	cases := map[string]bool{
		"rm -rf /tmp/cache":                 true,
		"rm -rf ~/.ssh":                     true,
		"rm -rf $HOME/foo":                  true,
		"rm -rf dist":                       false,
		"dd if=/dev/zero of=/dev/sda bs=1M": true,
		"mkfs.ext4 /dev/sdb1":               true,
		"sudo apt update":                   true,
		"reboot":                            true,
		"shutdown -h now":                   true,
		"ls":                                false,
		"cat /etc/passwd":                   false,
		"git clean -xdf":                    false,
		"ls; sudo whoami":                   true,
		":(){ :|:& };:":                     true,
		"chown -R root:root /":              true,
		// Device writes, in the shapes a shell actually accepts. ">= /dev/sda"
		// is the regression: the pattern had no whitespace tolerance after "=",
		// so this spaced form skipped the gate entirely while the unspaced one
		// was caught — a fail-open hole in a fail-closed check.
		">= /dev/sda":                true,
		">=  /dev/sda":               true,
		">=/dev/sda":                 true,
		"tee /dev/stdout > /dev/sda": true,
		"> /dev/sda":                 true,
		">>/dev/sda":                 true,
		"2> /dev/sda":                true,
		"echo hi > /tmp/out":         false,
		"echo hi > /tmp/a.conf":      false,
	}
	p, _ := NewPolicy("", nil, nil)
	for raw, want := range cases {
		if got := p.NeedsApproval(true, raw); got != want {
			t.Errorf("NeedsApproval(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestExtraPatterns(t *testing.T) {
	p, _ := NewPolicy("", []string{`git\s+push\s+(-f|--force)`}, nil)
	if !p.NeedsApproval(true, "git push -f origin main") {
		t.Fatal("extra pattern must match")
	}
	if p.NeedsApproval(true, "git push origin main") {
		t.Fatal("extra pattern must not over-match")
	}
}

func TestInWorkspace(t *testing.T) {
	p, _ := NewPolicy("", nil, []string{"/Users/s/projects"})
	if !p.InWorkspace("/Users/s/projects/mobile") {
		t.Fatal("subdir must be in workspace")
	}
	if !p.InWorkspace("/Users/s/projects") {
		t.Fatal("root itself must be in workspace")
	}
	if p.InWorkspace("/etc/passwd") {
		t.Fatal("/etc must be outside")
	}
	if p.InWorkspace("/Users/s/projects-other/x") {
		t.Fatal("sibling prefix must not match as subdir")
	}
	empty, _ := NewPolicy("", nil, nil)
	if !empty.InWorkspace("/anywhere") {
		t.Fatal("empty workspace must be permissive")
	}
}

// TestInWorkspaceAcceptsDotAndShortNames pins the paths the old len>=2 plus
// two-character test threw away. Each of these is inside the configured root, so
// treating it as out of scope denied the owner access to their own project —
// .git/config and .config being the ones that matter in practice, since a
// developer runs git inside an allowed root.
func TestInWorkspaceAcceptsDotAndShortNames(t *testing.T) {
	p, _ := NewPolicy("", nil, []string{"/Users/s/projects"})
	inside := []string{
		"/Users/s/projects/x",           // single-character child
		"/Users/s/projects/a",           // single-character child
		"/Users/s/projects/.env",        // leading dot
		"/Users/s/projects/a.conf",      // dot as the second character
		"/Users/s/projects/.git/config", // the project's own git directory
		"/Users/s/projects/.config/app", // a dotted config directory
		"/Users/s/projects/x/y/z",       // single-character first component
		"/Users/s/projects/...",         // a name that merely starts with dots
		"/Users/s/projects/.hidden/dir", // dot-prefixed directory
	}
	for _, path := range inside {
		if !p.InWorkspace(path) {
			t.Errorf("InWorkspace(%q) = false, want true: it is inside the root", path)
		}
	}
	// The escapes have to survive the looser test, or the policy is not a policy.
	outside := []string{
		"/Users/s/projects/../secrets",           // climbs out with ..
		"/Users/s/projects/mobile/../../secrets", // climbs out from deeper down
		"/Users/s/other/x",                       // a different directory entirely
		"/etc/passwd",
	}
	for _, path := range outside {
		if p.InWorkspace(path) {
			t.Errorf("InWorkspace(%q) = true, want false: it escapes the root", path)
		}
	}
}
