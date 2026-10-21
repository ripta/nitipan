package machine

import "testing"

func TestLocalHasHostName(t *testing.T) {
	m, err := Local()
	if err != nil {
		t.Fatalf("Local: %v", err)
	}

	if m.Host == "" {
		t.Error("host name is empty")
	}

	again, err := Local()
	if err != nil {
		t.Fatalf("Local: %v", err)
	}

	if again.ID != m.ID {
		t.Errorf("machine id changed between calls: %q and %q", m.ID, again.ID)
	}

	t.Logf("machine id %q", m.ID)
}

func TestFormatUUID(t *testing.T) {
	var u [16]byte
	for i := range u {
		u[i] = byte(i)
	}

	if got, want := FormatUUID(u), "00010203-0405-0607-0809-0a0b0c0d0e0f"; got != want {
		t.Errorf("FormatUUID = %q, want %q", got, want)
	}
}

func TestCleanID(t *testing.T) {
	if got := CleanID("  ABCDEF\n"); got != "abcdef" {
		t.Errorf("CleanID = %q", got)
	}
}
