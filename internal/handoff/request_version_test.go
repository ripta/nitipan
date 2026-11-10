package handoff

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// TestSubmitUsesKindSince tests that Submit writes the lowest request version that carries each
// request, using KindSince for the kind (Item 7).
func TestSubmitUsesKindSince(t *testing.T) {
	tests := []struct {
		kind string
		want int
	}{
		{KindClaim, 2},
		{KindRelease, 2},
		{KindBreak, 2},
		{KindRenew, 2},
		{KindList, 2},
		{KindFence, 2},
		{KindWhoHolds, 3},
		{KindWhoIs, 3},
		{KindMailSend, 4},
		{KindMailStatus, 4},
		{KindWhoAmI, 4},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, PendingDir), 0o700); err != nil {
				t.Fatal(err)
			}

			req := Request{
				ID:   state.NewID(),
				Kind: tt.kind,
				Requester: Requester{
					PID:     1234,
					Start:   record.ProcessStart{Sec: 100, Usec: 0},
					Machine: record.Machine{ID: "machine", Host: "host"},
				},
				CreatedAt: time.Now(),
			}

			if err := Submit(dir, req); err != nil {
				t.Fatalf("Submit failed: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(dir, PendingDir, req.ID+".json"))
			if err != nil {
				t.Fatalf("reading request: %v", err)
			}

			var written Request
			if err := json.Unmarshal(data, &written); err != nil {
				t.Fatalf("unmarshaling request: %v", err)
			}

			if written.Version != tt.want {
				t.Errorf("version = %d, want %d", written.Version, tt.want)
			}
		})
	}
}

// TestSubmitUsesRequestVersionForUnknownKind tests that Submit uses RequestVersion for kinds
// not in KindSince (Item 7).
func TestSubmitUsesRequestVersionForUnknownKind(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, PendingDir), 0o700); err != nil {
		t.Fatal(err)
	}

	req := Request{
		ID:   state.NewID(),
		Kind: "unknown-kind",
		Requester: Requester{
			PID:     1234,
			Start:   record.ProcessStart{Sec: 100, Usec: 0},
			Machine: record.Machine{ID: "machine", Host: "host"},
		},
		CreatedAt: time.Now(),
	}

	if err := Submit(dir, req); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, PendingDir, req.ID+".json"))
	if err != nil {
		t.Fatalf("reading request: %v", err)
	}

	var written Request
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("unmarshaling request: %v", err)
	}

	if written.Version != RequestVersion {
		t.Errorf("version = %d, want %d", written.Version, RequestVersion)
	}
}

// TestPresenceSupportsWithOldRequestVersion tests that a kind older than RequestVersion can still
// go to a node whose presence reports an older RequestVersion (Item 7).
func TestPresenceSupportsWithOldRequestVersion(t *testing.T) {
	p := Presence{
		RequestVersion: 3,
		Kinds:          []string{KindClaim, KindWhoHolds, KindWhoIs},
	}

	if err := p.Supports(KindClaim, 2); err != nil {
		t.Errorf("Supports(KindClaim, 2) failed: %v", err)
	}

	if err := p.Supports(KindWhoHolds, 3); err != nil {
		t.Errorf("Supports(KindWhoHolds, 3) failed: %v", err)
	}

	if err := p.Supports(KindWhoAmI, 4); err == nil {
		t.Error("Supports(KindWhoAmI, 4) should fail when presence lacks the kind")
	} else if !errors.Is(err, ErrNodeTooOld) {
		t.Errorf("expected ErrNodeTooOld, got %v", err)
	}

	if err := p.Supports(KindClaim, 5); err == nil {
		t.Error("Supports(KindClaim, 5) should fail when request version > presence version")
	} else if !errors.Is(err, ErrNodeTooOld) {
		t.Errorf("expected ErrNodeTooOld, got %v", err)
	}
}
