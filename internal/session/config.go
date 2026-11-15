package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// StartTime converts a process start to wall-clock time. Only macOS records one. A Linux start is
// ticks since boot, and converting it needs the boot time, which moves when the clock is stepped.
// The caller reads "unknown" for every check that needs the conversion.
func StartTime(s record.ProcessStart) (time.Time, bool) {
	if s.Platform != record.PlatformDarwin {
		return time.Time{}, false
	}

	return time.Unix(s.Sec, s.Usec*int64(time.Microsecond)).UTC(), true
}

func statMark(root string, k Key) (time.Time, error) {
	info, err := os.Lstat(filepath.Join(root, state.EndedDir, k.Name()))
	if err != nil {
		return time.Time{}, err
	}

	return info.ModTime().UTC(), nil
}

// defaultUnloadDelaySecs is what Codex 0.163.0 uses when the config sets nothing.
const defaultUnloadDelaySecs = 1800

// Checks are the two facts about the Codex config in force that a registration records.
type Checks struct {
	// UnloadDelaySecs is the thread_unload_delay_secs, or nil when it could not be read.
	UnloadDelaySecs *int

	// SessionEnd is CheckPresent, CheckAbsent, or CheckUnknown.
	SessionEnd string
}

func unknownChecks() Checks {
	return Checks{SessionEnd: CheckUnknown}
}

// ScanCodexConfig reads the config the way a Codex host read it at start.
//
// A change to the config needs a host restart, so a file older than the host's start is trusted. The
// second result says the scan trusted every file, which makes its values worth remembering.
//
// Codex rewrites config.toml when a TUI starts, which makes the file newer than that host without
// changing any setting nitipan checks. A newer file is therefore trusted for a value only when that
// value equals the one remembered from an earlier scan that trusted the file. A changed value, or
// none remembered, reads unknown. Only the user-level config.toml and hooks.json under the Codex
// home are read. Layered and profile config can still defeat the snapshot, which is why a
// registration records the result as a check and not a proof.
//
// nitipan has no TOML parser. The scan reads only the shapes Codex documents, and anything it
// cannot classify reads unknown.
func ScanCodexConfig(home, executable string, hostStart time.Time, haveStart bool, mem *ConfigMemory) (checks Checks, confirmed bool) {
	if !haveStart || !filepath.IsAbs(home) {
		return unknownChecks(), false
	}

	toml, tomlOld, tomlOK := readConfigFile(filepath.Join(home, "config.toml"), hostStart, haveStart)
	hooks, hooksOld, hooksOK := readConfigFile(filepath.Join(home, "hooks.json"), hostStart, haveStart)

	if !tomlOK || !hooksOK {
		return unknownChecks(), false
	}

	delay, delayOK := unloadDelay(toml)
	if delayOK {
		checks.UnloadDelaySecs = &delay
	}

	checks.SessionEnd = sessionEndCheck(toml, hooks, executable)

	if tomlOld && hooksOld {
		return checks, true
	}

	return mem.vouch(home, executable, checks), false
}

// readConfigFile returns a file's text, whether it is older than the host's start, and whether it
// could be read at all. An absent file reads as empty text and counts as old.
func readConfigFile(path string, hostStart time.Time, haveStart bool) (text string, old, ok bool) {
	info, err := os.Stat(path)
	if errors.Is(err, unix.ENOENT) {
		return "", haveStart, true
	}

	if err != nil || info.IsDir() {
		return "", false, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, false
	}

	return string(data), haveStart && info.ModTime().Before(hostStart), true
}

// ConfigMemory is the pair of checks nitipan last confirmed from config files older than a host's
// start, for one Codex home. It is the file sessions/config/<digest of the home>.json.
type ConfigMemory struct {
	Version   record.Version `json:"version"`
	CodexHome string         `json:"codex_home"`

	// UnloadDelaySecs is nil when the confirmed scan could not read it.
	UnloadDelaySecs *int `json:"unload_delay_secs"`

	// SessionEnd is CheckPresent, CheckAbsent, or CheckUnknown.
	SessionEnd string `json:"session_end"`

	// Executable is the nitipan that SessionEnd was matched against. A different executable never
	// compares equal, since a changed hook path is a changed setting.
	Executable string `json:"executable"`

	ConfirmedAt time.Time `json:"confirmed_at"`
}

// vouch keeps each value of checks that equals the remembered one, and reads the rest unknown.
func (m *ConfigMemory) vouch(home, executable string, checks Checks) Checks {
	out := unknownChecks()

	if m == nil || m.CodexHome != home {
		return out
	}

	if checks.UnloadDelaySecs != nil && m.UnloadDelaySecs != nil && *checks.UnloadDelaySecs == *m.UnloadDelaySecs {
		out.UnloadDelaySecs = checks.UnloadDelaySecs
	}

	if checks.SessionEnd != CheckUnknown && checks.SessionEnd == m.SessionEnd && m.Executable == executable {
		out.SessionEnd = checks.SessionEnd
	}

	return out
}

func (m *ConfigMemory) matches(home, executable string, checks Checks) bool {
	if m == nil || m.CodexHome != home || m.SessionEnd != checks.SessionEnd || m.Executable != executable {
		return false
	}

	if (m.UnloadDelaySecs == nil) != (checks.UnloadDelaySecs == nil) {
		return false
	}

	return m.UnloadDelaySecs == nil || *m.UnloadDelaySecs == *checks.UnloadDelaySecs
}

// configMemoryName names the memory file for a Codex home.
func configMemoryName(home string) string {
	sum := sha256.Sum256([]byte(home))

	return hex.EncodeToString(sum[:16]) + ".json"
}

// readConfigMemory returns the memory for a Codex home. A missing, undecodable, or higher-major file
// reads as no memory. The second result says a file exists that this binary must not overwrite: one
// it cannot decode, or one of a higher major or minor.
func (s *Service) readConfigMemory(home string) (mem *ConfigMemory, locked bool, err error) {
	data, err := state.ReadName(s.Root, state.ConfigDir, configMemoryName(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	var m ConfigMemory
	if json.Unmarshal(data, &m) != nil || writable(m.Version) != nil {
		return nil, true, nil
	}

	if m.CodexHome != home {
		return nil, false, nil
	}

	return &m, false, nil
}

// rememberConfig records confirmed checks, unless the memory already says the same. It leaves a file
// it must not overwrite alone.
func (s *Service) rememberConfig(l *state.Locked, home, executable string, checks Checks) error {
	prior, locked, err := s.readConfigMemory(home)
	if err != nil {
		return err
	}

	if locked || prior.matches(home, executable, checks) {
		return nil
	}

	data, err := encode(ConfigMemory{
		Version:         record.SessionVersion,
		CodexHome:       home,
		UnloadDelaySecs: checks.UnloadDelaySecs,
		SessionEnd:      checks.SessionEnd,
		Executable:      executable,
		ConfirmedAt:     s.now(),
	})
	if err != nil {
		return err
	}

	if err := l.ReplaceNamed(state.ConfigDir, configMemoryName(home), data); err != nil {
		return fmt.Errorf("remembering the Codex config checks: %w", err)
	}

	return nil
}

var (
	tableHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
	intAssign   = regexp.MustCompile(`^\s*thread_unload_delay_secs\s*=\s*([0-9_]+)\s*(#.*)?$`)
	anyAssign   = regexp.MustCompile(`^\s*thread_unload_delay_secs\b`)
	commandText = regexp.MustCompile(`command\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
	endAssign   = regexp.MustCompile(`^\s*(hooks\s*\.\s*)?"?SessionEnd"?\s*=`)
)

// unloadDelay finds the top-level thread_unload_delay_secs. An absent key means Codex's default.
// A key it cannot read as a plain integer is unknown.
func unloadDelay(toml string) (int, bool) {
	for _, line := range strings.Split(toml, "\n") {
		if tableHeader.MatchString(line) {
			break
		}

		if m := intAssign.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(strings.ReplaceAll(m[1], "_", ""))

			return n, err == nil
		}

		if anyAssign.MatchString(line) {
			return 0, false
		}
	}

	return defaultUnloadDelaySecs, true
}

// sessionEndCheck reports whether a SessionEnd hook runs this nitipan. It says absent only when
// neither file mentions SessionEnd at all. A mention it cannot match to this nitipan reads unknown,
// since the entry might be in a shape the scan does not read.
func sessionEndCheck(toml, hooksJSON, executable string) string {
	mentions := strings.Contains(toml, "SessionEnd") || strings.Contains(hooksJSON, "SessionEnd")

	if tomlRunsNitipan(toml, executable) || jsonRunsNitipan(hooksJSON, executable) {
		return CheckPresent
	}

	if mentions {
		return CheckUnknown
	}

	return CheckAbsent
}

// runsNitipan reports whether a hook command line runs this nitipan's session-end hook.
func runsNitipan(command, executable string) bool {
	plain := strings.NewReplacer("'", "", `"`, "").Replace(command)

	return strings.Contains(plain, executable+" hook session-end")
}

// tomlRunsNitipan scans the regions of a config.toml that belong to the SessionEnd event: every
// table whose name starts with hooks.SessionEnd, and a SessionEnd key under [hooks] or written as
// a dotted key.
func tomlRunsNitipan(toml, executable string) bool {
	var (
		inEnd   bool
		inHooks bool
		depth   int
		keyText strings.Builder
		keyOpen bool
	)

	scanRegion := func(text string) bool {
		for _, m := range commandText.FindAllStringSubmatch(text, -1) {
			if runsNitipan(unquote(m[1]), executable) {
				return true
			}
		}

		return false
	}

	for _, line := range strings.Split(toml, "\n") {
		if keyOpen {
			keyText.WriteString(line + "\n")
			depth += strings.Count(line, "[") + strings.Count(line, "{") - strings.Count(line, "]") - strings.Count(line, "}")

			if depth <= 0 {
				keyOpen = false

				if scanRegion(keyText.String()) {
					return true
				}
			}

			continue
		}

		if m := tableHeader.FindStringSubmatch(line); m != nil {
			name := strings.ReplaceAll(m[1], " ", "")
			inEnd = name == "hooks.SessionEnd" || strings.HasPrefix(name, "hooks.SessionEnd.")
			inHooks = name == "hooks"

			continue
		}

		if inEnd {
			if scanRegion(line) {
				return true
			}

			continue
		}

		if endAssign.MatchString(line) && (inHooks || strings.Contains(line, "hooks")) {
			keyText.Reset()
			keyText.WriteString(line + "\n")

			depth = strings.Count(line, "[") + strings.Count(line, "{") - strings.Count(line, "]") - strings.Count(line, "}")
			if depth <= 0 {
				if scanRegion(line) {
					return true
				}

				continue
			}

			keyOpen = true
		}
	}

	return false
}

func unquote(s string) string {
	if strings.HasPrefix(s, `'`) {
		return strings.Trim(s, `'`)
	}

	out, err := strconv.Unquote(s)
	if err != nil {
		return s
	}

	return out
}

// hooksFile is the part of hooks.json that holds SessionEnd entries. Codex accepts the events at
// the top level or under a hooks key.
type hooksFile struct {
	Hooks      map[string][]hookGroup `json:"hooks"`
	SessionEnd []hookGroup            `json:"SessionEnd"`
}

type hookGroup struct {
	Hooks []struct {
		Command string `json:"command"`
	} `json:"hooks"`
}

func jsonRunsNitipan(text, executable string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}

	var f hooksFile
	if json.Unmarshal([]byte(text), &f) != nil {
		return false
	}

	groups := slices.Concat(f.Hooks["SessionEnd"], f.SessionEnd)

	for _, g := range groups {
		for _, h := range g.Hooks {
			if runsNitipan(h.Command, executable) {
				return true
			}
		}
	}

	return false
}
