package mailbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// Sent is the sender's note on a message it sent: its address and the stage the broker last reported.
// A stage never wakes the sender. The note lets the sender's next nitipan call mention it once.
type Sent struct {
	Version record.Version `json:"version"`
	ID      string         `json:"id"`
	To      string         `json:"to"`
	Stage   string         `json:"stage"`
	At      time.Time      `json:"at"`

	// Shown is the stage a nitipan call already mentioned.
	Shown string `json:"shown,omitempty"`
}

// SentRetention is how long a note stays after its last change.
const SentRetention = 8 * 24 * time.Hour

// WriteSent writes the sender's note.
func WriteSent(root string, s Sent) error {
	if !ValidID(s.ID) {
		return fmt.Errorf("message %q: %w", s.ID, ErrBadName)
	}

	s.Version = Version

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding the sent note of %s: %w", s.ID, err)
	}

	return atomicfile.Write(filepath.Join(root, SentDir), s.ID+fileSuffix, data)
}

// ReadSent reads one note. A missing note returns an error matching fs.ErrNotExist.
func ReadSent(root, id string) (Sent, error) {
	data, err := state.ReadName(root, SentDir, id+fileSuffix)
	if err != nil {
		return Sent{}, err
	}

	var s Sent
	if err := json.Unmarshal(data, &s); err != nil {
		return Sent{}, fmt.Errorf("decoding the sent note of %s: %w", id, err)
	}

	if err := checkVersion(s.Version); err != nil {
		return Sent{}, err
	}

	return s, nil
}

// SetStage records a later stage in a note. It creates no note for a message this machine did not send,
// and never moves a stage back.
func SetStage(root, id, stage string, at time.Time) error {
	s, err := ReadSent(root, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if s.Version.Minor > Version.Minor {
		return fmt.Errorf("sent note %s: %w", id, ErrNotRewritten)
	}

	if rank(stage) <= rank(s.Stage) {
		return nil
	}

	s.Stage, s.At = stage, at.UTC()

	return WriteSent(root, s)
}

func rank(stage string) int {
	switch stage {
	case "stored":
		return 1
	case "read":
		return 2
	}

	return 0
}

// Unshown lists the notes whose stage advanced since a nitipan call last mentioned it. It also drops
// notes older than the retention.
func Unshown(root string, now time.Time) ([]Sent, error) {
	names, err := state.ListNames(root, SentDir)
	if err != nil {
		return nil, err
	}

	var out []Sent

	for _, n := range names {
		id, ok := trimSuffix(n)
		if !ok {
			continue
		}

		s, err := ReadSent(root, id)
		if err != nil {
			continue
		}

		if now.Sub(s.At) > SentRetention {
			_ = removeSent(root, id)

			continue
		}

		if s.Version.Minor > Version.Minor {
			continue
		}

		if s.Stage != s.Shown && rank(s.Stage) > rank(s.Shown) {
			out = append(out, s)
		}
	}

	return out, nil
}

// MarkShown records that a call mentioned the note's stage.
func MarkShown(root string, s Sent) error {
	s.Shown = s.Stage

	return WriteSent(root, s)
}

func trimSuffix(name string) (string, bool) {
	if len(name) <= len(fileSuffix) || name[len(name)-len(fileSuffix):] != fileSuffix {
		return "", false
	}

	return name[:len(name)-len(fileSuffix)], true
}

func removeSent(root, id string) error {
	return removeFile(filepath.Join(root, SentDir, id+fileSuffix))
}
