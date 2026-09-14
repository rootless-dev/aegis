package configs_test

import (
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
)

// Thirty-two bytes of ASCII, so the fixture is readable in a failure message.
const testMasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestCryptoAcceptsAThirtyTwoByteKey(t *testing.T) {
	cfg := &configs.Crypto{MasterKey: testMasterKey}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

func TestCryptoRefusesAMissingKey(t *testing.T) {
	cfg := &configs.Crypto{}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if !strings.Contains(err.Error(), "openssl rand -base64 32") {
		t.Errorf("want the error to say how to generate a key, got %q", err)
	}
}

func TestCryptoRefusesAKeyOfTheWrongLength(t *testing.T) {
	// Thirty-one bytes.
	cfg := &configs.Crypto{MasterKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZQ=="}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if !strings.Contains(err.Error(), "31") {
		t.Errorf("want the error to report the length it found, got %q", err)
	}
}

func TestCryptoRefusesAKeyThatIsNotBase64(t *testing.T) {
	cfg := &configs.Crypto{MasterKey: "not base64 at all!!"}

	if err := cfg.Validate(); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestCryptoValidatesThePreviousKeyToo(t *testing.T) {
	cfg := &configs.Crypto{MasterKey: testMasterKey, MasterKeyPrevious: "nope"}

	if err := cfg.Validate(); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestCryptoBytesDecodes(t *testing.T) {
	cfg := &configs.Crypto{MasterKey: testMasterKey}

	decoded, err := cfg.Bytes()
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(decoded) != 32 {
		t.Errorf("want 32 bytes, got %d", len(decoded))
	}
}

func TestCryptoPreviousBytesIsNilWhenUnset(t *testing.T) {
	cfg := &configs.Crypto{MasterKey: testMasterKey}

	decoded, err := cfg.PreviousBytes()
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if decoded != nil {
		t.Errorf("want nil, got %v", decoded)
	}
}

func TestApplicationValidateReportsAMissingCryptoSection(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileProd
	cfg.PublicURL = "https://auth.example.com"
	cfg.Crypto = nil

	err := cfg.Validate()
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if !strings.Contains(err.Error(), "crypto configuration is missing") {
		t.Errorf("want the missing section reported, got %q", err)
	}
}
