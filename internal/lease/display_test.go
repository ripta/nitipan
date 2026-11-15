package lease

import (
	"slices"
	"testing"

	"github.com/space-armada/nitipan/internal/keys"
)

func keyList(paths ...string) []keys.Key {
	out := make([]keys.Key, len(paths))
	for i, p := range paths {
		out[i] = keys.Key{Path: p}
	}

	return out
}

type marksTest struct {
	Name    string
	Query   []string
	Own     []string
	Subs    []subLease
	Owner   bool
	Grantor []string
}

var marksTests = []marksTest{
	{
		Name:  "a filter on the parent's checkout makes it owner and grantor of the delegated part",
		Query: []string{"/repo"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid", Keys: keyList("/repo/src")}},
		Owner: true, Grantor: []string{"kid"},
	},
	{
		Name:  "a filter on the delegated key makes the parent only the grantor",
		Query: []string{"/repo/src"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid", Keys: keyList("/repo/src")}},
		Owner: false, Grantor: []string{"kid"},
	},
	{
		Name:  "a filter inside the delegated key makes the parent only the grantor",
		Query: []string{"/repo/src/api"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid", Keys: keyList("/repo/src")}},
		Owner: false, Grantor: []string{"kid"},
	},
	{
		Name:  "a filter on the parent's own part leaves it owner of everything and grantor of nothing",
		Query: []string{"/repo/lib"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid", Keys: keyList("/repo/src")}},
		Owner: true,
	},
	{
		Name:  "a filter that spans two sub-leases is grantor of both and owner of what they leave",
		Query: []string{"/repo"}, Own: []string{"/repo"},
		Subs: []subLease{
			{ID: "a", Keys: keyList("/repo/src")},
			{ID: "b", Keys: keyList("/repo/docs")},
		},
		Owner: true, Grantor: []string{"a", "b"},
	},
	{
		Name:  "a filter on one of two sub-leases is grantor of that one only",
		Query: []string{"/repo/docs"}, Own: []string{"/repo"},
		Subs: []subLease{
			{ID: "a", Keys: keyList("/repo/src")},
			{ID: "b", Keys: keyList("/repo/docs")},
		},
		Owner: false, Grantor: []string{"b"},
	},
	{
		Name:  "a full handoff leaves the parent no part of its own",
		Query: []string{"/repo"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid", Keys: keyList("/repo")}},
		Owner: false, Grantor: []string{"kid"},
	},
	{
		Name:  "a parent whose every key is delegated owns nothing, though the filter is wider",
		Query: []string{"/repo"}, Own: []string{"/repo/src", "/repo/docs"},
		Subs: []subLease{
			{ID: "a", Keys: keyList("/repo/src")},
			{ID: "b", Keys: keyList("/repo/docs")},
		},
		Owner: false, Grantor: []string{"a", "b"},
	},
	{
		Name:  "a filter that shares only a prefix with the delegated key is not an overlap",
		Query: []string{"/repo/srcx"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid", Keys: keyList("/repo/src")}},
		Owner: true,
	},
	{
		Name:  "a lease with no sub-leases is the owner of what it matches",
		Query: []string{"/repo/src"}, Own: []string{"/repo"},
		Owner: true,
	},
	{
		Name:  "a sub-lease whose keys are unknown makes the parent grantor of nothing and owner of the match",
		Query: []string{"/repo/src"}, Own: []string{"/repo"},
		Subs:  []subLease{{ID: "kid"}},
		Owner: true,
	},
}

func TestMarks(t *testing.T) {
	for _, test := range marksTests {
		t.Run(test.Name, func(t *testing.T) {
			owner, grantor := marks(keyList(test.Query...), keyList(test.Own...), test.Subs, nil)

			if owner != test.Owner {
				t.Errorf("owner = %v, want %v", owner, test.Owner)
			}

			if !slices.Equal(grantor, test.Grantor) {
				t.Errorf("grantor of = %v, want %v", grantor, test.Grantor)
			}
		})
	}
}
