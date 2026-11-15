package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/space-armada/nitipan/internal/keys"
)

// FootprintKind says whether a footprint lists entries, blocks nothing, or blocks everything.
//
// The zero value is FootprintAll, so a footprint that is absent or unreadable fails safe.
type FootprintKind int

// Footprint kinds.
const (
	FootprintAll FootprintKind = iota
	FootprintNone
	FootprintEntries
)

// Entry types. A reader treats a type it does not know as blocking everything.
const (
	EntryPath = "path"
	EntryName = "name"
)

// Entry is one typed item in a footprint.
type Entry struct {
	Type string `json:"type"`

	// Path entries.
	Path      string `json:"path,omitempty"`
	FoldCase  bool   `json:"fold_case,omitempty"`
	Normalize bool   `json:"normalize,omitempty"`

	// Name entries.
	Name string `json:"name,omitempty"`
}

// Known reports whether this binary understands the entry type.
func (e Entry) Known() bool {
	return e.Type == EntryPath || e.Type == EntryName
}

// Key returns the path entry as a key.
func (e Entry) Key() keys.Key {
	return keys.Key{Path: e.Path, FoldCase: e.FoldCase, Normalize: e.Normalize}
}

// PathEntry builds a path entry from a key.
func PathEntry(k keys.Key) Entry {
	return Entry{Type: EntryPath, Path: k.Path, FoldCase: k.FoldCase, Normalize: k.Normalize}
}

// NameEntry builds a name entry, which compares by exact match.
func NameEntry(name string) Entry {
	return Entry{Type: EntryName, Name: name}
}

// Footprint is what a record blocks.
type Footprint struct {
	Kind    FootprintKind
	Entries []Entry
}

// PathFootprint builds the footprint of a path record from its canonical keys.
func PathFootprint(ks []keys.Key) Footprint {
	entries := make([]Entry, len(ks))
	for i, k := range ks {
		entries[i] = PathEntry(k)
	}

	return Footprint{Kind: FootprintEntries, Entries: entries}
}

// ResourceFootprint builds the footprint of a resource record from its name.
func ResourceFootprint(name string) Footprint {
	return Footprint{Kind: FootprintEntries, Entries: []Entry{NameEntry(name)}}
}

// NoneFootprint blocks nothing.
func NoneFootprint() Footprint {
	return Footprint{Kind: FootprintNone}
}

// AllFootprint blocks everything.
func AllFootprint() Footprint {
	return Footprint{Kind: FootprintAll}
}

// Overlaps reports whether a claim blocked by f would conflict with one blocked by o.
//
// An entry of a type this binary does not know overlaps every footprint that blocks something. A
// path entry and a name entry never overlap.
func (f Footprint) Overlaps(o Footprint, finder keys.BoundaryFinder) bool {
	if f.Kind == FootprintNone || o.Kind == FootprintNone {
		return false
	}

	if f.Kind == FootprintAll || o.Kind == FootprintAll {
		return true
	}

	for _, a := range f.Entries {
		for _, b := range o.Entries {
			if entriesOverlap(a, b, finder) {
				return true
			}
		}
	}

	return false
}

func entriesOverlap(a, b Entry, finder keys.BoundaryFinder) bool {
	if !a.Known() || !b.Known() {
		return true
	}

	switch {
	case a.Type == EntryPath && b.Type == EntryPath:
		return keys.Overlap(a.Key(), b.Key(), finder)
	case a.Type == EntryName && b.Type == EntryName:
		return a.Name == b.Name
	default:
		return false
	}
}

// SamePathKeys reports whether the footprint's path entries are exactly the given keys, ignoring
// order. It compares stored values and resolves nothing.
func (f Footprint) SamePathKeys(ks []keys.Key) bool {
	if f.Kind != FootprintEntries || len(f.Entries) != len(ks) {
		return false
	}

	want := make([]Entry, len(ks))
	for i, k := range ks {
		want[i] = PathEntry(k)
	}

	got := append([]Entry(nil), f.Entries...)

	sortEntries(got)
	sortEntries(want)

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

// SameName reports whether the footprint is exactly one name entry equal to the given name. An empty
// name never matches.
func (f Footprint) SameName(name string) bool {
	return name != "" && f.Kind == FootprintEntries && len(f.Entries) == 1 && f.Entries[0] == NameEntry(name)
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}

		if a.Path != b.Path {
			return a.Path < b.Path
		}

		return a.Name < b.Name
	})
}

// MarshalJSON writes "none", "all", or the entry list.
func (f Footprint) MarshalJSON() ([]byte, error) {
	switch f.Kind {
	case FootprintNone:
		return []byte(`"none"`), nil
	case FootprintEntries:
		entries := f.Entries
		if entries == nil {
			entries = []Entry{}
		}

		data, err := json.Marshal(entries)
		if err != nil {
			return nil, fmt.Errorf("encoding footprint entries: %w", err)
		}

		return data, nil
	default:
		return []byte(`"all"`), nil
	}
}

// UnmarshalJSON reads a footprint. It never fails: whatever it cannot read counts as "all", so a
// record with a footprint from the future still blocks.
func (f *Footprint) UnmarshalJSON(data []byte) error {
	*f = parseFootprint(data)

	return nil
}

// parseFootprint accepts only the exact spellings it knows. An empty entry list counts as "all",
// because "none" is never inferred.
func parseFootprint(data []byte) Footprint {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return AllFootprint()
	}

	switch data[0] {
	case '"':
		var s string
		if json.Unmarshal(data, &s) == nil && s == "none" {
			return NoneFootprint()
		}
	case '[':
		var entries []Entry
		if json.Unmarshal(data, &entries) == nil && len(entries) > 0 {
			return Footprint{Kind: FootprintEntries, Entries: entries}
		}
	}

	return AllFootprint()
}
