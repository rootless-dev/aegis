package configs_test

import (
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
	"gopkg.in/yaml.v3"
)

const (
	testAdminEmail    = "admin@example.com"
	testAdminPassword = "correct-horse-battery-staple"
)

func TestBootstrapIsNotConfiguredByDefault(t *testing.T) {
	cfg := configs.Default()

	if cfg.Bootstrap.Admin.Configured() {
		t.Error("want no bootstrap credentials by default")
	}

	if err := cfg.Bootstrap.Validate(); err != nil {
		t.Errorf("an installation that sets nothing must be valid, got %v", err)
	}
}

func TestBootstrapAdminIsConfiguredWithAnEmailAndAPassword(t *testing.T) {
	cfg := &configs.BootstrapAdmin{Email: testAdminEmail, Username: "admin", Password: testAdminPassword}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if !cfg.Configured() {
		t.Error("want the credentials reported as supplied")
	}
}

func TestBootstrapAdminRefusesAnIncompleteSet(t *testing.T) {
	cases := map[string]struct {
		cfg   configs.BootstrapAdmin
		wants []string
	}{
		"a password without an email": {
			cfg:   configs.BootstrapAdmin{Password: testAdminPassword},
			wants: []string{"AEGIS_BOOTSTRAP_ADMIN_EMAIL"},
		},
		"an email without a password": {
			cfg:   configs.BootstrapAdmin{Email: testAdminEmail},
			wants: []string{"AEGIS_BOOTSTRAP_ADMIN_PASSWORD", "AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE"},
		},
		"a username alone": {
			cfg: configs.BootstrapAdmin{Username: "admin"},
			wants: []string{
				"AEGIS_BOOTSTRAP_ADMIN_EMAIL", "AEGIS_BOOTSTRAP_ADMIN_PASSWORD", "AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.cfg.Configured() {
				t.Error("an incomplete set must not count as supplied")
			}

			err := tc.cfg.Validate()
			if err == nil {
				t.Fatal("want an error, got none")
			}

			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("want the error to name %s, got %q", want, err)
				}
			}
		})
	}
}

func TestBootstrapAdminEmailShape(t *testing.T) {
	// 254 bytes exactly: the local part pads what the domain leaves.
	longest := strings.Repeat("a", 254-len("@example.com")) + "@example.com"

	cases := map[string]struct {
		email string
		valid bool
	}{
		"plain":                {testAdminEmail, true},
		"a single label host":  {"admin@localhost", true},
		"at the limit":         {longest, true},
		"one byte over":        {"a" + longest, false},
		"no at sign":           {"admin.example.com", false},
		"two at signs":         {"admin@site@example.com", false},
		"nothing before":       {"@example.com", false},
		"nothing after":        {"admin@", false},
		"a space inside":       {"ad min@example.com", false},
		"a trailing newline":   {testAdminEmail + "\n", false},
		"a non-breaking space": {"admin@example.com ", false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &configs.BootstrapAdmin{Email: tc.email, Password: testAdminPassword}

			err := cfg.Validate()
			if tc.valid && err != nil {
				t.Errorf("want no error, got %v", err)
			}

			if !tc.valid && err == nil {
				t.Error("want an error, got none")
			}
		})
	}
}

func TestBootstrapAdminUsernameShape(t *testing.T) {
	cases := map[string]struct {
		username string
		valid    bool
	}{
		"absent":        {"", true},
		"plain":         {"admin", true},
		"at the limit":  {strings.Repeat("a", 64), true},
		"one byte over": {strings.Repeat("a", 65), false},
		// An identifier with an "@" is an email, and the two cannot collide.
		"an at sign":       {"admin@example.com", false},
		"a space":          {"the admin", false},
		"a tab at the end": {"admin\t", false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &configs.BootstrapAdmin{Email: testAdminEmail, Username: tc.username, Password: testAdminPassword}

			err := cfg.Validate()
			if tc.valid && err != nil {
				t.Errorf("want no error, got %v", err)
			}

			if !tc.valid && err == nil {
				t.Error("want an error, got none")
			}
		})
	}
}

func TestBootstrapAdminPasswordLength(t *testing.T) {
	atLimit := &configs.BootstrapAdmin{Email: testAdminEmail, Password: strings.Repeat("p", 1024)}
	if err := atLimit.Validate(); err != nil {
		t.Errorf("want 1024 bytes accepted, got %v", err)
	}

	over := &configs.BootstrapAdmin{Email: testAdminEmail, Password: strings.Repeat("p", 1025)}

	err := over.Validate()
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if !strings.Contains(err.Error(), "1025") {
		t.Errorf("want the error to report the length it found, got %q", err)
	}
}

// Deliberate: the credential is created marked for mandatory change, and
// refusing it would leave the installation with no administrator.
func TestBootstrapAdminAcceptsAWeakPassword(t *testing.T) {
	cfg := &configs.BootstrapAdmin{Email: testAdminEmail, Password: "a"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("want no strength rule, got %v", err)
	}
}

func TestBootstrapAdminNeverEchoesThePassword(t *testing.T) {
	const marker = "s3cr3t-marker"

	cases := map[string]configs.BootstrapAdmin{
		"without an email":      {Password: marker},
		"with a bad email":      {Email: "not-an-email", Password: marker},
		"with a bad username":   {Email: testAdminEmail, Username: "a b", Password: marker},
		"over the length limit": {Email: testAdminEmail, Password: strings.Repeat(marker, 100)},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			err := cfg.Validate()
			if err == nil {
				t.Fatal("want an error, got none")
			}

			if strings.Contains(err.Error(), marker) {
				t.Errorf("the password must never be in an error, got %q", err)
			}
		})
	}
}

func TestApplicationValidateReportsBootstrapProblems(t *testing.T) {
	cfg := configs.Default()
	cfg.Bootstrap.Admin.Email = testAdminEmail

	err := cfg.Validate()
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if !strings.Contains(err.Error(), "bootstrap: admin password is required") {
		t.Errorf("want the bootstrap section validated with the others, got %q", err)
	}
}

func TestBootstrapEmailAndUsernameDecodeFromYAML(t *testing.T) {
	cfg := configs.Default()

	document := "bootstrap:\n  admin:\n    email: admin@example.com\n    username: admin\n"
	if err := yaml.Unmarshal([]byte(document), cfg); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if cfg.Bootstrap.Admin.Email != testAdminEmail || cfg.Bootstrap.Admin.Username != "admin" {
		t.Errorf("want the email and the username decoded, got %+v", cfg.Bootstrap.Admin)
	}
}

// What an operator is left with after commenting the values out.
func TestBootstrapSurvivesAKeyWithNoBody(t *testing.T) {
	for _, document := range []string{"bootstrap:\n", "bootstrap:\n  admin:\n"} {
		cfg := configs.Default()

		if err := yaml.Unmarshal([]byte(document), cfg); err != nil {
			t.Fatalf("%q: want no error, got %v", document, err)
		}

		if err := cfg.Bootstrap.Validate(); err != nil {
			t.Errorf("%q: want an empty section to be valid, got %v", document, err)
		}
	}
}

func TestBootstrapPasswordCannotComeFromYAML(t *testing.T) {
	cfg := configs.Default()

	decoder := yaml.NewDecoder(strings.NewReader(
		"bootstrap:\n  admin:\n    email: admin@example.com\n    password: from-the-file\n",
	))
	// What the builder decodes with.
	decoder.KnownFields(true)

	if err := decoder.Decode(cfg); err == nil {
		t.Error("want a password key in the file to be rejected")
	}

	if cfg.Bootstrap.Admin.Password != "" {
		t.Errorf("want the password left unset, got %q", cfg.Bootstrap.Admin.Password)
	}
}
