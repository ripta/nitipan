package broker

import (
	"strings"
	"testing"
	"time"
)

func TestCreateUserAssignsIDAndDerivesUserLabel(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.CreateUser("test@example.com", now)
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
	user, err := h.b.CreateUser("user@example.com", now)
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
	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
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

func TestCheckPairingCodeAcceptsCorrectCode(t *testing.T) {
	h := newHarness(t, nil)

	code := GeneratePairingCode()
	hash := HashPairingCode(code)

	now := time.Now()
	expiresAt := now.Add(PairingCodeExpiry)

	if err := h.b.StorePairingCode("test-label", hash, expiresAt); err != nil {
		t.Fatal(err)
	}

	ok, err := h.b.CheckPairingCode("test-label", code, "user1", now)
	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Error("correct code was rejected")
	}
}

func TestCheckPairingCodeRejectsWrongCode(t *testing.T) {
	h := newHarness(t, nil)

	code := GeneratePairingCode()
	hash := HashPairingCode(code)

	now := time.Now()
	expiresAt := now.Add(PairingCodeExpiry)

	if err := h.b.StorePairingCode("test-label", hash, expiresAt); err != nil {
		t.Fatal(err)
	}

	ok, err := h.b.CheckPairingCode("test-label", "wrong-wrong-wrong-wrong", "user1", now)
	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Error("wrong code was accepted")
	}
}

func TestCheckPairingCodeRejectsExpiredCode(t *testing.T) {
	h := newHarness(t, nil)

	code := GeneratePairingCode()
	hash := HashPairingCode(code)

	now := time.Now()
	expiresAt := now.Add(PairingCodeExpiry)

	if err := h.b.StorePairingCode("test-label", hash, expiresAt); err != nil {
		t.Fatal(err)
	}

	afterExpiry := expiresAt.Add(time.Second)
	ok, err := h.b.CheckPairingCode("test-label", code, "user1", afterExpiry)
	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Error("expired code was accepted")
	}
}

func TestCheckPairingCodeLocksOutAfterMaxWrongAttempts(t *testing.T) {
	h := newHarness(t, nil)

	code := GeneratePairingCode()
	hash := HashPairingCode(code)

	now := time.Now()
	expiresAt := now.Add(PairingCodeExpiry)

	if err := h.b.StorePairingCode("test-label", hash, expiresAt); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < MaxWrongPairingAttempts; i++ {
		_, _ = h.b.CheckPairingCode("test-label", "wrong-wrong-wrong-wrong", "user1", now)
	}

	_, err := h.b.CheckPairingCode("test-label", code, "user1", now)
	if err == nil {
		t.Error("user was not locked out after max wrong attempts")
	}
}

func TestCheckPairingCodeTracksWrongAttemptsPerUser(t *testing.T) {
	h := newHarness(t, nil)

	code := GeneratePairingCode()
	hash := HashPairingCode(code)

	now := time.Now()
	expiresAt := now.Add(PairingCodeExpiry)

	if err := h.b.StorePairingCode("test-label", hash, expiresAt); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < MaxWrongPairingAttempts; i++ {
		_, _ = h.b.CheckPairingCode("test-label", "wrong-wrong-wrong-wrong", "user1", now)
	}

	ok, err := h.b.CheckPairingCode("test-label", code, "user2", now)
	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Error("user2 was locked out by user1's wrong attempts")
	}
}
