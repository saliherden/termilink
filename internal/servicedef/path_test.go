package servicedef

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A real directory that is guaranteed to exist on the test machine, used where
// a case needs a PATH entry that survives the existence check.
func existingDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestSplitPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	t.Run("splits on the OS separator", func(t *testing.T) {
		got := splitPath(strings.Join([]string{"/a", "/b", "/c"}, sep))
		want := []string{"/a", "/b", "/c"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("drops empty and whitespace entries", func(t *testing.T) {
		// A trailing separator is normal in PATH and must not become an
		// empty entry, which would later clean to "." and mean the job's
		// working directory.
		got := splitPath("/a" + sep + sep + "  /b  " + sep)
		want := []string{"/a", "/b"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := splitPath(""); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	if runtime.GOOS == "windows" {
		t.Run("a drive letter is not a separator", func(t *testing.T) {
			// The bug this guards: splitting a Windows PATH on ":" would
			// turn "C:\tools" into "C" and "\tools", and every program
			// installed under the drive would vanish from the job.
			got := splitPath(`C:\tools;D:\more;C:\Windows`)
			want := []string{`C:\tools`, `D:\more`, `C:\Windows`}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// adequateBase is a PATH that passes the adequacy check: enough entries, and
// all of the base directories present. The directories the tool-installed ones
// stand in for must really exist, because ResolvePath drops entries that do
// not — that check is what removes sandbox and stale paths, so a test that
// invented non-existent directories would be testing the wrong thing.
func adequateBase(t *testing.T, extra ...string) string {
	t.Helper()
	// Two filler directories so the result clears minPathEntries: a real
	// terminal PATH runs to a dozen or more entries, and four base directories
	// plus one tool directory is not what one looks like.
	entries := append([]string{}, extra...)
	entries = append(entries, existingDir(t), existingDir(t))
	entries = append(entries, BasePathEntries...)
	return strings.Join(entries, string(os.PathListSeparator))
}

func TestResolvePathPicksInheritedWhenAdequate(t *testing.T) {
	// An adequate inherited PATH is the normal case: the user ran the
	// installer from their own terminal, so it already has Homebrew, nvm and
	// everything else their shell had.
	brew := existingDir(t)
	got, rep := ResolvePath(adequateBase(t, brew), "/only/one")
	if rep.Source != "inherited environment" {
		t.Errorf("source = %q, want the inherited environment", rep.Source)
	}
	if !strings.Contains(got, brew) {
		t.Errorf("PATH lost the Homebrew entry: %q", got)
	}
}

func TestResolvePathFallsBackToLoginShell(t *testing.T) {
	// The failure this exists for: the installer was invoked from a context
	// with a stripped PATH, and the login shell has the real one.
	nvm := existingDir(t)
	sep := string(os.PathListSeparator)
	got, rep := ResolvePath("/usr/bin"+sep+"/bin", adequateBase(t, nvm))
	if rep.Source != "login shell" {
		t.Errorf("source = %q, want the login shell", rep.Source)
	}
	if !strings.Contains(got, nvm) {
		t.Errorf("PATH lost the nvm entry: %q", got)
	}
}

func TestResolvePathLeavesOutTheCandidateItRejected(t *testing.T) {
	// The winning candidate is the whole result. Folding the loser in as well
	// produces a PATH that came from neither source while the report names one,
	// and that report is the only explanation given for what a job was started
	// with. It is also how a stripped IDE PATH ends up quietly supplemented
	// after the user was told their own environment was used.
	brew := existingDir(t)
	loginOnly := existingDir(t)
	got, rep := ResolvePath(adequateBase(t, brew), adequateBase(t, loginOnly))
	if rep.Source != "inherited environment" {
		t.Fatalf("source = %q, want the inherited environment", rep.Source)
	}
	if strings.Contains(got, loginOnly) {
		t.Errorf("PATH took an entry from the rejected login shell: %q", got)
	}
}

func TestResolvePathReportsEachDroppedEntryOnce(t *testing.T) {
	// A stale directory is usually stale in both candidates, and an interactive
	// login shell often repeats it within its own PATH. Listed per occurrence it
	// reads as several findings and buries the one that matters.
	brew := existingDir(t)
	stale := filepath.Join(t.TempDir(), "sandbox", "bin")
	sep := string(os.PathListSeparator)
	_, rep := ResolvePath(
		adequateBase(t, brew)+sep+stale,
		adequateBase(t, brew)+sep+stale+sep+stale,
	)
	counts := map[string]int{}
	for _, d := range rep.Dropped {
		counts[d]++
	}
	for line, n := range counts {
		if n > 1 {
			t.Errorf("dropped line %q appears %d times, want 1:\n%s", line, n, rep.String())
		}
	}
}

func TestResolvePathMergesWhenNeitherIsUsable(t *testing.T) {
	// Both candidates are short but hold different things. Merging keeps both
	// rather than guessing which one the user meant.
	brew := existingDir(t)
	local := existingDir(t)
	sep := string(os.PathListSeparator)
	got, rep := ResolvePath("/usr/bin"+sep+"/bin"+sep+brew, "/usr/bin"+sep+"/bin"+sep+local)
	if !strings.HasPrefix(rep.Source, "merged") {
		t.Errorf("source = %q, want a merge", rep.Source)
	}
	for _, want := range []string{brew, local} {
		if !strings.Contains(got, want) {
			t.Errorf("PATH %q lost %q", got, want)
		}
	}
}

func TestResolvePathAlwaysHasBaseEntries(t *testing.T) {
	// Even with nothing usable, the job gets a shell that works. An agent
	// whose shell cannot run ls is indistinguishable from a hung one when all
	// you have is a Telegram chat.
	got, _ := ResolvePath("", "")
	entries := splitPath(got)
	for _, want := range BasePathEntries {
		if !contains(entries, want) {
			t.Errorf("PATH %q is missing the base entry %q", got, want)
		}
	}
}

func TestResolvePathDropsDirectoriesThatDoNotExist(t *testing.T) {
	// A sandbox or wrapper process contributes paths that mean nothing to a
	// normal login. Passing them through suggests tools are installed when
	// they are not, so they are dropped — and reported, because a silently
	// shortened PATH is how a user ends up wondering where gh went.
	ghost := "/nonexistent/agent/bootstrap/bin"
	brew := existingDir(t)
	got, rep := ResolvePath(adequateBase(t, ghost, brew), "")

	if strings.Contains(got, ghost) {
		t.Errorf("PATH kept a directory that does not exist: %q", got)
	}
	found := false
	for _, d := range rep.Dropped {
		if strings.Contains(d, ghost) {
			found = true
		}
	}
	if !found {
		t.Errorf("the dropped directory was not reported: %v", rep.Dropped)
	}
	if !strings.Contains(got, brew) {
		t.Errorf("the usable entries were dropped along with it: %q", got)
	}
}

func TestResolvePathRepairsTildePrefix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	// Some .zshrc setups put a literal "~" into PATH. Nothing downstream
	// expands it: the shell reads the tilde as part of a filename, so every
	// command in that directory is "not found".
	realHome := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(realHome, 0o755); err != nil {
		t.Skipf("cannot create a test directory under the home directory: %v", err)
	}

	brew := existingDir(t)
	got, rep := ResolvePath(adequateBase(t, "~/.local/bin", brew), "")

	if strings.Contains(got, "~") {
		t.Errorf("PATH still contains a literal tilde: %q", got)
	}
	if !strings.Contains(got, realHome) {
		t.Errorf("PATH %q is missing the expanded %q", got, realHome)
	}
	if len(rep.Repaired) == 0 {
		t.Errorf("the repair was not reported: %v", rep.Repaired)
	}
}

func TestResolvePathDropsTildeInTheMiddle(t *testing.T) {
	// "~" anywhere but the front cannot be repaired: there is no base to
	// expand it against, and keeping it would leave a path that never resolves.
	bad := "/opt/~backup/bin"
	got, rep := ResolvePath(adequateBase(t, bad, existingDir(t)), "")

	if strings.Contains(got, "~") {
		t.Errorf("PATH kept an unrepairable tilde: %q", got)
	}
	if len(rep.Dropped) == 0 {
		t.Errorf("the drop was not reported: %v", rep.Dropped)
	}
}

func TestResolvePathDeduplicates(t *testing.T) {
	// An interactive login shell re-sources .zshrc on top of an already
	// populated PATH. With a few version managers installed the same directory
	// can appear four times, which bloats the generated file and makes the
	// effective PATH impossible to read.
	existing := existingDir(t)
	got, _ := ResolvePath(adequateBase(t, existing, existing, existing), "")

	entries := splitPath(got)
	seen := map[string]int{}
	for _, e := range entries {
		seen[filepath.Clean(e)]++
	}
	for entry, n := range seen {
		if n > 1 {
			t.Errorf("entry %q appears %d times in %q", entry, n, got)
		}
	}
}

func TestResolvePathKeepsTheFirstOccurrenceOrder(t *testing.T) {
	// Order is not cosmetic: an earlier entry wins when two directories both
	// provide the same binary, which is how a per-project override stays in
	// front of a global install.
	first := existingDir(t)
	second := existingDir(t)
	got, _ := ResolvePath(adequateBase(t, first, second), "")

	entries := splitPath(got)
	if len(entries) == 0 || entries[0] != first {
		t.Fatalf("the first entry is %v, want %q to stay in front", entries, first)
	}
	if len(entries) < 2 || entries[1] != second {
		t.Errorf("the second entry is %v, want %q to follow", entries, second)
	}
}

func TestResolvePathKeepsRelativeEntries(t *testing.T) {
	// A relative entry is legal and resolves against the job's working
	// directory. Dropping it would change behaviour the user asked for, so it
	// is preserved even though the rest of the PATH is absolute.
	brew := existingDir(t)
	got, _ := ResolvePath(adequateBase(t, "bin", brew), "")

	entries := splitPath(got)
	found := false
	for _, e := range entries {
		if e == "bin" {
			found = true
		}
	}
	if !found {
		t.Errorf("a relative entry was dropped: %q", got)
	}
}

func TestServiceValidate(t *testing.T) {
	abs := existingDir(t)
	base := func() *Service {
		return &Service{
			Label:      DefaultLabel,
			Executable: "/usr/local/bin/termilink",
			WorkingDir: abs,
			Args:       []string{"start"},
		}
	}

	t.Run("accepts a complete description", func(t *testing.T) {
		if err := base().Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("rejects a relative executable", func(t *testing.T) {
		s := base()
		s.Executable = "termilink"
		// A relative path is resolved by the supervisor's PATH, which for a
		// job started outside an interactive login is minimal — so this is
		// the difference between starting and not.
		if err := s.Validate(); err == nil {
			t.Fatal("expected an error for a relative executable")
		}
	})

	t.Run("rejects a relative working directory", func(t *testing.T) {
		s := base()
		s.WorkingDir = "."
		if err := s.Validate(); err == nil {
			t.Fatal("expected an error for a relative working directory")
		}
	})

	t.Run("rejects a relative state file", func(t *testing.T) {
		s := base()
		s.StateFile = "state.json"
		if err := s.Validate(); err == nil {
			t.Fatal("expected an error for a relative state file")
		}
	})

	t.Run("rejects an empty label", func(t *testing.T) {
		s := base()
		s.Label = ""
		if err := s.Validate(); err == nil {
			t.Fatal("expected an error for an empty label")
		}
	})

	t.Run("rejects a control character in an argument", func(t *testing.T) {
		s := base()
		s.Args = []string{"start\nsomething"}
		// An argument with a newline can terminate a command line in a
		// generated shell script, which on Windows is exactly how a
		// description would turn into extra commands.
		if err := s.Validate(); err == nil {
			t.Fatal("expected an error for a newline in an argument")
		}
	})

	t.Run("an empty state file is allowed", func(t *testing.T) {
		s := base()
		s.StateFile = ""
		if err := s.Validate(); err != nil {
			t.Fatalf("an unset state file must not fail validation: %v", err)
		}
	})
}

func TestNewFillsDefaults(t *testing.T) {
	s := New(DefaultLabel, "/usr/local/bin/termilink", "/Users/u/termilink", "/var/lib/termilink/state.json", "/Users/u/Library/Logs/termilink", []string{"PATH=/usr/bin"})
	if s.ThrottleSeconds != DefaultThrottleSeconds {
		t.Errorf("ThrottleSeconds = %d, want %d", s.ThrottleSeconds, DefaultThrottleSeconds)
	}
	if len(s.Args) != 1 || s.Args[0] != "start" {
		t.Errorf("Args = %v, want [start]", s.Args)
	}
}

func TestReportStringNamesTheSource(t *testing.T) {
	rep := Report{
		Source:   "inherited environment",
		Repaired: []string{"~/.local/bin -> /home/u/.local/bin"},
		Dropped:  []string{"/nonexistent/bin (does not exist)"},
	}
	got := rep.String()
	for _, want := range []string{"inherited environment", "repaired", "dropped", "~/.local/bin", "/nonexistent/bin"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}
