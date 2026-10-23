package lease

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// SlotConfigFile is the name of the slot config in the state directory.
const SlotConfigFile = "slots.json"

// slotConfigVersion is the only version of the slot config this binary reads.
const slotConfigVersion = 1

// defaultSlots is the count of a resource name with no configured count. One holder is exclusive.
const defaultSlots = 1

// SlotConfig is the per-machine count of holders each resource name admits.
//
// A config that cannot be used as a whole falls back to a count of 1 for every name. A part of it is
// never applied, because a half-read file would admit more holders than its author meant.
type SlotConfig struct {
	// Path is the absolute path of the file, which every warning and `list` name.
	Path string

	counts map[string]int

	// Problems holds every error found, in a stable order. The config is unusable when any exist.
	Problems []string
}

// LoadSlotConfig reads slots.json from the state directory at root. A missing file is a valid config
// with no entries. It reads without taking the lock, so callers that need a consistent view hold
// the lock themselves.
func LoadSlotConfig(root string) *SlotConfig {
	cfg := &SlotConfig{Path: filepath.Join(root, SlotConfigFile), counts: map[string]int{}}

	data, err := state.ReadName(root, "", SlotConfigFile)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg
	case err != nil:
		cfg.Problems = []string{err.Error()}

		return cfg
	}

	cfg.parse(data)

	return cfg
}

func (cfg *SlotConfig) parse(data []byte) {
	var top map[string]json.RawMessage

	dec := json.NewDecoder(bytes.NewReader(data))

	if err := dec.Decode(&top); err != nil {
		cfg.Problems = []string{fmt.Sprintf("the file is not a JSON object: %v", err)}

		return
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		cfg.Problems = []string{"the file holds more than one JSON value"}

		return
	}

	var problems []string

	var unknown []string

	for field := range top {
		if field != "version" && field != "slots" {
			unknown = append(unknown, field)
		}
	}

	sort.Strings(unknown)

	for _, field := range unknown {
		problems = append(problems, fmt.Sprintf("unknown field %q", field))
	}

	problems = append(problems, checkSlotVersion(top["version"])...)

	counts, countProblems := parseSlotCounts(top["slots"])
	problems = append(problems, countProblems...)

	if len(problems) > 0 {
		cfg.Problems = problems

		return
	}

	cfg.counts = counts
}

func checkSlotVersion(raw json.RawMessage) []string {
	if raw == nil {
		return []string{`missing "version"; want 1`}
	}

	var v int

	if err := json.Unmarshal(raw, &v); err != nil {
		return []string{fmt.Sprintf(`"version" is %s; want the integer 1`, raw)}
	}

	if v != slotConfigVersion {
		return []string{fmt.Sprintf(`unknown "version" %d; this nitipan reads version %d`, v, slotConfigVersion)}
	}

	return nil
}

func parseSlotCounts(raw json.RawMessage) (counts map[string]int, problems []string) {
	counts = map[string]int{}

	if raw == nil {
		return counts, nil
	}

	var entries map[string]json.RawMessage

	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, []string{`"slots" must be an object from resource names to counts`}
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		if err := keys.ValidateResource(name); err != nil {
			problems = append(problems, fmt.Sprintf("slots entry %q: %v", name, err))

			continue
		}

		n, err := strconv.Atoi(strings.TrimSpace(string(entries[name])))
		if err != nil || n < 1 {
			problems = append(problems, fmt.Sprintf("slots entry %q: the count %s must be an integer of at least 1", name,
				entries[name]))

			continue
		}

		counts[name] = n
	}

	return counts, problems
}

// Usable reports whether the config was read in full.
func (cfg *SlotConfig) Usable() bool {
	return len(cfg.Problems) == 0
}

// Count returns the configured count of a name. A name with no entry, or any name when the config is
// unusable, is exclusive.
func (cfg *SlotConfig) Count(name string) int {
	if !cfg.Usable() {
		return defaultSlots
	}

	if n, ok := cfg.counts[name]; ok {
		return n
	}

	return defaultSlots
}

// Warning is the text every call prints while the config is unusable, or empty when it is usable.
func (cfg *SlotConfig) Warning() string {
	if cfg.Usable() {
		return ""
	}

	return fmt.Sprintf("the slot config %s cannot be used, so every resource name is exclusive. Ask the user to fix it. "+
		"Problems:\n  %s", cfg.Path, strings.Join(cfg.Problems, "\n  "))
}

// recordSlots is the count a record was admitted under. A record with no count is exclusive.
func recordSlots(rec *record.Record) int {
	if rec.Domain.Slots > 0 {
		return rec.Domain.Slots
	}

	return defaultSlots
}

// slotView is what the live local holders of one resource name say about its count.
type slotView struct {
	// Limit is the number of holders admission allows: the config's count, lowered to the count of
	// every live holder.
	Limit int

	// Holders are the live local records that hold the name.
	Holders []*entry
}

// slotViews groups the local resource records by name. Another machine's record is another
// resource, so it counts toward nothing. A holder whose liveness is unknown counts as alive.
func (c *call) slotViews(cfg *SlotConfig) map[string]*slotView {
	views := map[string]*slotView{}

	for _, en := range c.entries {
		name, ok := en.resource()
		if !ok || !liveness.SameMachine(c.e.Machine, en.Rec.Machine) {
			continue
		}

		view := views[name]
		if view == nil {
			view = &slotView{Limit: cfg.Count(name)}
			views[name] = view
		}

		if !en.Live.TreatAsAlive() {
			continue
		}

		view.Holders = append(view.Holders, en)
		view.Limit = min(view.Limit, recordSlots(en.Rec))
	}

	return views
}
