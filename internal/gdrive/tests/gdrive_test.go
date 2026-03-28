package gdrive_test

import (
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/fitbase/fitbase/internal/gdrive"
)

func TestTokenToJSON_RoundTrip(t *testing.T) {
	original := &oauth2.Token{
		AccessToken:  "access-abc",
		RefreshToken: "refresh-xyz",
		TokenType:    "Bearer",
		Expiry:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	encoded, err := gdrive.TokenToJSON(original)
	if err != nil {
		t.Fatalf("TokenToJSON: %v", err)
	}
	if encoded == "" {
		t.Fatal("TokenToJSON returned empty string")
	}

	decoded, err := gdrive.TokenFromJSON(encoded)
	if err != nil {
		t.Fatalf("TokenFromJSON: %v", err)
	}

	if decoded.AccessToken != original.AccessToken {
		t.Errorf("AccessToken = %q, want %q", decoded.AccessToken, original.AccessToken)
	}
	if decoded.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", decoded.RefreshToken, original.RefreshToken)
	}
	if !decoded.Expiry.Equal(original.Expiry) {
		t.Errorf("Expiry = %v, want %v", decoded.Expiry, original.Expiry)
	}
}

func TestTokenFromJSON_Invalid(t *testing.T) {
	_, err := gdrive.TokenFromJSON("this is not json {{{")
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestTokenFromJSON_Empty(t *testing.T) {
	_, err := gdrive.TokenFromJSON("")
	if err == nil {
		t.Error("expected error for empty string, got nil")
	}
}
