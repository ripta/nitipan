package broker

import (
	"strings"
	"testing"
	"time"
)

func TestAdmitUserAssignsIDAndDerivesUserLabel(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.AdmitUser(createIssuer, "test@example.com", "test@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if user.ID == "" {
		t.Error("user ID is empty")
	}

	if user.UserLabel == "" {
		t.Error("user label is empty")
	}

	if user.LabelDerivation != UserLabelDerivation {
		t.Errorf("label derivation = %d, want %d", user.LabelDerivation, UserLabelDerivation)
	}

	if user.Email != "test@example.com" {
		t.Errorf("email = %q, want %q", user.Email, "test@example.com")
	}

	if !user.CreatedAt.Equal(now) {
		t.Errorf("created at = %v, want %v", user.CreatedAt, now)
	}

	wordCount := strings.Count(user.UserLabel, "-")
	if wordCount != 3 {
		t.Errorf("user label has %d dashes, want 3 (four words)", wordCount)
	}
}

func TestAdmitUserMapsIssuerSubToUser(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.AdmitUser("https://issuer.example", "sub123", "user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if user.ID == "" {
		t.Error("user ID is empty")
	}

	key := issuerSubKey("https://issuer.example", "sub123")
	if h.b.store.Data().IssuerSubs[key] != user.ID {
		t.Errorf("issuer sub not mapped to user ID")
	}
}

func TestAdmitUserRefusesDuplicateIssuerSub(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	if _, err := h.b.AdmitUser("https://issuer.example", "sub123", "first@example.com", now); err != nil {
		t.Fatal(err)
	}

	if _, err := h.b.AdmitUser("https://issuer.example", "sub123", "second@example.com", now); err == nil {
		t.Error("duplicate issuer sub was admitted")
	}
}

func TestLinkIssuerSubMapsToExistingUser(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.AdmitUser(createIssuer, "user@example.com", "user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.b.LinkIssuerSub("https://issuer.example", "sub456", user.ID); err != nil {
		t.Fatal(err)
	}

	key := issuerSubKey("https://issuer.example", "sub456")
	if h.b.store.Data().IssuerSubs[key] != user.ID {
		t.Error("issuer sub not linked to user ID")
	}
}

func TestLinkIssuerSubRefusesExistingMapping(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user1, err := h.b.AdmitUser(createIssuer, "user1@example.com", "user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.AdmitUser(createIssuer, "user2@example.com", "user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.b.LinkIssuerSub("https://issuer.example", "sub789", user1.ID); err != nil {
		t.Fatal(err)
	}

	if err := h.b.LinkIssuerSub("https://issuer.example", "sub789", user2.ID); err == nil {
		t.Error("issuer sub was linked to a second user")
	}
}

// TestGeneratePairingCodeProducesFourWords proves a pairing code is four words joined by dashes, as the
// node prints it and the user types it. It catches a generator that returns an empty code or changes its
// shape, which the page's form and the user's instructions no longer match.
func TestGeneratePairingCodeProducesFourWords(t *testing.T) {
	code := GeneratePairingCode()

	if code == "" {
		t.Error("pairing code is empty")
	}

	wordCount := strings.Count(code, "-")
	if wordCount != 3 {
		t.Errorf("pairing code has %d dashes, want 3 (four words)", wordCount)
	}
}

// TestHashPairingCodeProducesConsistentHash proves the hash a node sends for a code is stable and is a
// SHA-256 hex digest. It catches a salted or truncated hash, which the broker's check of the typed code
// would never match.
func TestHashPairingCodeProducesConsistentHash(t *testing.T) {
	code := "word1-word2-word3-word4"

	hash1 := HashPairingCode(code)
	hash2 := HashPairingCode(code)

	if hash1 != hash2 {
		t.Error("hash is not consistent for the same code")
	}

	if hash1 == "" {
		t.Error("hash is empty")
	}

	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA-256 hex)", len(hash1))
	}
}
