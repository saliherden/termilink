package servicedef

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BasePathEntries are the directories a job can always count on. They are
// appended to every resolved PATH so a job still has a working shell if the
// captured environment turns out to be stale — an agent whose shell cannot run
// ls looks identical to a hung one, and that is an expensive thing to debug
// from a Telegram chat.
var BasePathEntries = []string{
	"/usr/bin",
	"/bin",
	"/usr/sbin",
	"/sbin",
}

// minPathEntries is the point below which a captured PATH is treated as
// untrustworthy. A login shell that was not interactive can return as few as
// four entries, and none of them need to be useful: /usr/bin alone is enough
// for that shell to start.
const minPathEntries = 6

// adequatePath reports whether a captured PATH is rich enough to use on its
// own. Anything missing the base entries is rejected outright, since that
// means the capture failed rather than that the environment is small.
func adequatePath(entries []string) bool {
	if len(entries) < minPathEntries {
		return false
	}
	for _, want := range BasePathEntries {
		if !contains(entries, want) {
			return false
		}
	}
	return true
}

// contains reports whether entries holds want, comparing cleaned paths so that
// a trailing slash or a "." segment does not defeat the match.
func contains(entries []string, want string) bool {
	cleanWant := filepath.Clean(want)
	for _, e := range entries {
		if filepath.Clean(e) == cleanWant {
			return true
		}
	}
	return false
}

// splitPath splits a PATH-style string on the OS list separator. On Windows
// the separator is ";" and a drive letter contains ":", so a literal split
// there would tear "C:\tools" in half; the OS value is what decides, not a
// hardcoded colon.
func splitPath(value string) []string {
	if value == "" {
		return nil
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	var out []string
	for _, part := range strings.Split(value, sep) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// The Source values a Report can carry. They are exported because the caller
// has to compare against one to decide how much of its own explanation belongs
// in the report.
const (
	SourceInherited = "inherited environment"
	SourceLogin     = "login shell"
	SourceMerged    = "merged (neither candidate was usable alone)"
	SourceOverride  = "explicit --path override"
)

// Report explains what a PATH resolution did, so an install that produces a
// job with an unexpected PATH can be understood from the install output rather
// than from the generated file.
type Report struct {
	// Source names where the winning PATH came from: "inherited", "login
	// shell", or "merged" when neither candidate was usable alone.
	Source string
	// Note is optional detail from the caller about the source that won —
	// which shell was asked, for instance. It is kept out of Source so that
	// Source stays a value the caller can compare against.
	Note string
	// Dropped lists entries removed and why. Never empty on success, since a
	// PATH that quietly lost its Homebrew or nvm entries is the failure this
	// whole function exists to make visible.
	Dropped []string
	// Repaired lists entries changed before use, such as a "~" prefix expanded
	// to the home directory.
	Repaired []string
}

// dedupeNotes collapses repeated report lines.
//
// Both candidates are cleaned before one is chosen, so a directory that is
// stale in the inherited PATH is very often stale in the login shell too, and
// without this it is listed once per candidate — including once per duplicate
// the login shell itself produced. Listing the same line four times reads as
// four separate findings and hides the one that matters.
func (r *Report) dedupeNotes() {
	r.Repaired = dedupeStrings(r.Repaired)
	r.Dropped = dedupeStrings(r.Dropped)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// String renders the report for display.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "PATH source: %s", r.Source)
	if r.Note != "" {
		fmt.Fprintf(&b, " (%s)", r.Note)
	}
	if n := len(r.Repaired); n > 0 {
		fmt.Fprintf(&b, "\nrepaired (%d):", n)
		for _, d := range r.Repaired {
			fmt.Fprintf(&b, "\n  %s", d)
		}
	}
	if n := len(r.Dropped); n > 0 {
		fmt.Fprintf(&b, "\ndropped (%d):", n)
		for _, d := range r.Dropped {
			fmt.Fprintf(&b, "\n  %s", d)
		}
	}
	return b.String()
}

// ResolvePath picks the PATH a service job should run with and returns it
// joined, along with a report describing what happened.
//
// The problem is that there is no single correct answer. An init system starts
// a job with a minimal PATH of its own, which is why capturing the interactive
// environment matters at all — but neither source is reliable alone:
//
//   - The inherited PATH depends entirely on how the installer was invoked. Run
//     it from a terminal and it is right. Run it from an IDE, a cron job or a
//     CI step and it may already be reduced.
//   - A non-interactive login shell (zsh -lc) does not read .zshrc, so on a
//     machine where Homebrew or nvm sets itself up there, the result has no
//     gh, no node and no java.
//   - An interactive login shell (zsh -lic) does read .zshrc, but it also
//     re-sources whatever already set PATH, so entries come out duplicated,
//     and it can carry paths from a sandbox or wrapper process that are
//     meaningless to a normal login.
//
// So the candidates are tried in order, the first adequate one wins, and if
// none is adequate they are merged. The result is then cleaned and topped up
// with BasePathEntries.
//
// The os.Stat check is why a stale or sandbox-only path disappears: a PATH
// entry pointing at a directory that does not exist is dead weight at best and
// misleading at worst, since it suggests a tool is installed when it is not.
// A missing entry is dropped and reported rather than passed through silently.
//
// This function is pure: it does no I/O beyond stat-ing candidate directories,
// so it can be tested directly.
func ResolvePath(inherited, login string) (string, Report) {
	var rep Report

	inheritedEntries := cleanEntries(splitPath(inherited), &rep)
	loginEntries := cleanEntries(splitPath(login), &rep)

	// The winner decides the result on its own. Merging the loser in as well
	// would produce a PATH that matches neither source while the report names
	// one of them — and the report is the only explanation this command gives
	// for what went into a long-lived job.
	var chosen []string
	switch {
	case adequatePath(inheritedEntries):
		rep.Source = SourceInherited
		chosen = inheritedEntries
	case adequatePath(loginEntries):
		rep.Source = SourceLogin
		chosen = loginEntries
	default:
		rep.Source = SourceMerged
		chosen = merge(inheritedEntries, loginEntries)
	}

	// Top up with the base entries last, so a candidate that happened to list
	// /usr/bin in the middle cannot stop it from being added in the normal
	// place. This also guarantees the shell has a minimum environment even
	// when every captured entry was dropped.
	merged := append(chosen, BasePathEntries...)

	out := dedupe(merged, &rep)
	rep.dedupeNotes()
	return strings.Join(out, string(os.PathListSeparator)), rep
}

// cleanEntries normalizes candidate entries and drops the unusable ones,
// recording why. Entries are cleaned rather than rejected for being relative:
// an empty or "." entry is legal in PATH and means the current directory, so
// removing it silently would change behaviour in a way nobody asked for.
func cleanEntries(entries []string, rep *Report) []string {
	home, homeErr := os.UserHomeDir()
	var out []string
	for _, e := range entries {
		// A literal "~" in PATH is not expanded by anything downstream: the
		// shell treats the tilde as part of a filename, so a command in that
		// directory is simply not found. Some .zshrc setups put it there
		// literally, so expand it rather than losing the directory.
		//
		// The expansion falls through to the existence check below instead of
		// continuing. Continuing would make the repair an exemption from the
		// check, so a "~" pointing at something that is not there would be
		// repaired straight back into a dead entry — which is how a path can
		// appear in the "dropped" list and in the result at the same time.
		if e == "~" || strings.HasPrefix(e, "~/") {
			if homeErr != nil || home == "" {
				rep.Dropped = append(rep.Dropped, fmt.Sprintf("%s (no home directory to expand ~)", e))
				continue
			}
			expanded := filepath.Join(home, strings.TrimPrefix(e, "~"))
			rep.Repaired = append(rep.Repaired, fmt.Sprintf("%s -> %s", e, expanded))
			e = expanded
		}
		if strings.Contains(e, "~") {
			rep.Dropped = append(rep.Dropped, fmt.Sprintf("%s (unexpanded ~ in the middle of a path)", e))
			continue
		}
		if !filepath.IsAbs(e) {
			// Kept, not dropped: a relative entry is legal and resolves
			// against the job's working directory. Filtering it would remove
			// something the user deliberately put there.
			out = append(out, filepath.Clean(e))
			continue
		}
		if _, err := os.Stat(e); err != nil {
			rep.Dropped = append(rep.Dropped, fmt.Sprintf("%s (does not exist)", e))
			continue
		}
		out = append(out, filepath.Clean(e))
	}
	return out
}

// merge concatenates two entry lists, preferring the order of the first.
func merge(primary, secondary []string) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	out = append(out, primary...)
	out = append(out, secondary...)
	return out
}

// dedupe removes repeated entries, keeping the first occurrence so that the
// order the user actually had is preserved. Duplicates are common and not
// harmless: an interactive login shell re-sources .zshrc on top of an already
// populated PATH, and on a machine with a few version managers that can
// produce the same directory four times over.
func dedupe(entries []string, rep *Report) []string {
	seen := make(map[string]bool, len(entries))
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		key := filepath.Clean(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}
