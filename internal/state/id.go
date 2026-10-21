package state

import "github.com/oklog/ulid/v2"

// NewID returns a new lease ID, a ULID. IDs sort by creation time and are never reused, so a stale
// or mistyped ID names nothing.
func NewID() string {
	return ulid.Make().String()
}
