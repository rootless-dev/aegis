package passhash_test

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/rootless-dev/aegis/internal/infra/passhash"
)

var cheap = passhash.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

// From the reference implementation's own suite, src/test.c in
// P-H-C/phc-winner-argon2: password "password", salt "somesalt".
var referenceVectors = []struct {
	params  passhash.Params
	encoded string
}{
	{
		params:  passhash.Params{Memory: 256, Time: 2, Parallelism: 1, SaltLength: 8, KeyLength: 32},
		encoded: "$argon2id$v=19$m=256,t=2,p=1$c29tZXNhbHQ$nf65EOgLrQMR/uIPnA4rEsF5h7TKyQwu9U1bMCHGi/4",
	},
	{
		params:  passhash.Params{Memory: 256, Time: 2, Parallelism: 2, SaltLength: 8, KeyLength: 32},
		encoded: "$argon2id$v=19$m=256,t=2,p=2$c29tZXNhbHQ$bQk8UB/VmZZF4Oo79iDXuL5/0ttZwg2f/5U52iv1cDc",
	},
}

func newHasher(t *testing.T, params passhash.Params) *passhash.Hasher {
	t.Helper()

	hasher, err := passhash.NewWithParams(params)
	if err != nil {
		t.Fatalf("building the hasher: %v", err)
	}

	return hasher
}

func mustHash(t *testing.T, hasher *passhash.Hasher, password string) string {
	t.Helper()

	encoded, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	return encoded
}

func TestRoundTrip(t *testing.T) {
	hasher := newHasher(t, cheap)

	for _, password := range []string{"correct horse battery staple", "", "with$separators,and=signs", strings.Repeat("a", 4096)} {
		encoded := mustHash(t, hasher, password)

		ok, needsRehash, err := hasher.Verify(password, encoded)
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}

		if !ok {
			t.Errorf("want %q to match its own hash %q", password, encoded)
		}

		if needsRehash {
			t.Errorf("want no rehash for a hash made with the same parameters, got one for %q", encoded)
		}
	}
}

func TestVerifyRefusesAWrongPassword(t *testing.T) {
	hasher := newHasher(t, cheap)
	encoded := mustHash(t, hasher, "right")

	for _, password := range []string{"wrong", "", "right ", "Right"} {
		ok, needsRehash, err := hasher.Verify(password, encoded)
		if err != nil {
			t.Fatalf("want a wrong password to be no error, got %v", err)
		}

		if ok || needsRehash {
			t.Errorf("want %q refused with no rehash, got ok=%t needsRehash=%t", password, ok, needsRehash)
		}
	}
}

func TestHashSaltsEveryCall(t *testing.T) {
	hasher := newHasher(t, cheap)

	if first, second := mustHash(t, hasher, "same"), mustHash(t, hasher, "same"); first == second {
		t.Errorf("want two different hashes, got %q twice", first)
	}
}

func TestHashEmitsThePHCFormat(t *testing.T) {
	shape := regexp.MustCompile(`^\$argon2id\$v=19\$m=64,t=1,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)

	if encoded := mustHash(t, newHasher(t, cheap), "password"); !shape.MatchString(encoded) {
		t.Errorf("want a PHC string with unpadded base64, got %q", encoded)
	}
}

func TestEncodingMatchesTheReferenceImplementation(t *testing.T) {
	hasher := newHasher(t, cheap)

	for _, vector := range referenceVectors {
		salt := []byte("somesalt")
		key := argon2.IDKey(
			[]byte("password"), salt,
			vector.params.Time, vector.params.Memory, vector.params.Parallelism, vector.params.KeyLength,
		)

		if got := passhash.Encode(vector.params, salt, key); got != vector.encoded {
			t.Errorf("want %q, got %q", vector.encoded, got)
		}

		ok, needsRehash, err := hasher.Verify("password", vector.encoded)
		if err != nil || !ok || !needsRehash {
			t.Errorf("want %q verified and due a rehash, got ok=%t needsRehash=%t err=%v",
				vector.encoded, ok, needsRehash, err)
		}

		if ok, _, err := hasher.Verify("passworD", vector.encoded); err != nil || ok {
			t.Errorf("want another password refused for %q, got ok=%t err=%v", vector.encoded, ok, err)
		}
	}
}

func TestVerifyAsksForARehashOnEveryDifferingParameter(t *testing.T) {
	current := passhash.Params{Memory: 128, Time: 2, Parallelism: 2, SaltLength: 16, KeyLength: 32}
	hasher := newHasher(t, current)

	cases := map[string]func(*passhash.Params){
		"less memory":      func(p *passhash.Params) { p.Memory = 64 },
		"more memory":      func(p *passhash.Params) { p.Memory = 256 },
		"less time":        func(p *passhash.Params) { p.Time = 1 },
		"more time":        func(p *passhash.Params) { p.Time = 3 },
		"less parallelism": func(p *passhash.Params) { p.Parallelism = 1 },
		"more parallelism": func(p *passhash.Params) { p.Parallelism = 4 },
		"shorter salt":     func(p *passhash.Params) { p.SaltLength = 8 },
		"longer salt":      func(p *passhash.Params) { p.SaltLength = 32 },
		"shorter key":      func(p *passhash.Params) { p.KeyLength = 16 },
		"longer key":       func(p *passhash.Params) { p.KeyLength = 64 },
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			other := current
			change(&other)

			encoded := mustHash(t, newHasher(t, other), "password")

			ok, needsRehash, err := hasher.Verify("password", encoded)
			if err != nil || !ok || !needsRehash {
				t.Errorf("want a match due a rehash, got ok=%t needsRehash=%t err=%v", ok, needsRehash, err)
			}

			ok, needsRehash, err = hasher.Verify("wrong", encoded)
			if err != nil || ok || needsRehash {
				t.Errorf("want a mismatch to never ask for a rehash, got ok=%t needsRehash=%t err=%v",
					ok, needsRehash, err)
			}
		})
	}
}

func phc(costs string, saltLength, keyLength int) string {
	return "$argon2id$v=19$" + costs + "$" +
		base64.RawStdEncoding.EncodeToString(make([]byte, saltLength)) + "$" +
		base64.RawStdEncoding.EncodeToString(make([]byte, keyLength))
}

func TestVerifyRefusesWhatItCannotEvaluate(t *testing.T) {
	const (
		salt = "c29tZXNhbHQ"
		key  = "nf65EOgLrQMR/uIPnA4rEsF5h7TKyQwu9U1bMCHGi/4"
		tail = "$m=256,t=2,p=1$" + salt + "$" + key
	)

	cases := []struct {
		name    string
		encoded string
		want    error
	}{
		{"empty", "", passhash.ErrMalformed},
		{"not a phc string", "password", passhash.ErrMalformed},
		{"only separators", "$$$$$", passhash.ErrMalformed},
		{"no leading separator", "argon2id$v=19" + tail, passhash.ErrMalformed},
		{"leading space", " $argon2id$v=19" + tail, passhash.ErrMalformed},

		{"argon2i", "$argon2i$v=19" + tail, passhash.ErrUnsupported},
		{"argon2d", "$argon2d$v=19" + tail, passhash.ErrUnsupported},
		{"uppercase algorithm", "$ARGON2ID$v=19" + tail, passhash.ErrUnsupported},
		{"bcrypt", "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", passhash.ErrUnsupported},
		{"version 16", "$argon2id$v=16" + tail, passhash.ErrUnsupported},
		{"version with a leading zero", "$argon2id$v=019" + tail, passhash.ErrUnsupported},
		{"empty version", "$argon2id$v=" + tail, passhash.ErrUnsupported},

		{"missing version", "$argon2id" + tail, passhash.ErrMalformed},
		{"version without its name", "$argon2id$19" + tail, passhash.ErrMalformed},
		{"missing costs", "$argon2id$v=19$" + salt + "$" + key, passhash.ErrMalformed},
		{"missing key", "$argon2id$v=19$m=256,t=2,p=1$" + salt, passhash.ErrMalformed},
		{"extra field", "$argon2id$v=19" + tail + "$extra", passhash.ErrMalformed},
		{"trailing separator", "$argon2id$v=19" + tail + "$", passhash.ErrMalformed},
		{"trailing newline", "$argon2id$v=19" + tail + "\n", passhash.ErrMalformed},
		{"trailing space", "$argon2id$v=19" + tail + " ", passhash.ErrMalformed},

		{"reordered costs", "$argon2id$v=19$t=2,m=256,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"missing cost", "$argon2id$v=19$m=256,t=2$" + salt + "$" + key, passhash.ErrMalformed},
		{"duplicate cost", "$argon2id$v=19$m=256,m=256,t=2,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"unknown cost", "$argon2id$v=19$m=256,t=2,p=1,x=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"keyid in place of a cost", "$argon2id$v=19$m=256,t=2,keyid=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"empty cost", "$argon2id$v=19$m=,t=2,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"negative cost", "$argon2id$v=19$m=256,t=-2,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"signed cost", "$argon2id$v=19$m=+256,t=2,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"cost with a leading zero", "$argon2id$v=19$m=0256,t=2,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"hexadecimal cost", "$argon2id$v=19$m=0x100,t=2,p=1$" + salt + "$" + key, passhash.ErrMalformed},
		{"cost with a space", "$argon2id$v=19$m=256, t=2,p=1$" + salt + "$" + key, passhash.ErrMalformed},

		{"padded salt", "$argon2id$v=19$m=256,t=2,p=1$" + salt + "=$" + key, passhash.ErrMalformed},
		{"padded key", "$argon2id$v=19$m=256,t=2,p=1$" + salt + "$" + key + "=", passhash.ErrMalformed},
		{"url alphabet", "$argon2id$v=19$m=256,t=2,p=1$" + salt + "$" + strings.ReplaceAll(key, "/", "_"), passhash.ErrMalformed},
		{"nonzero trailing bits", "$argon2id$v=19$m=256,t=2,p=1$c29tZXNhbHR$" + key, passhash.ErrMalformed},
		{"newline inside the key", "$argon2id$v=19$m=256,t=2,p=1$" + salt + "$" + key[:10] + "\n" + key[10:], passhash.ErrMalformed},
		{"empty salt", "$argon2id$v=19$m=256,t=2,p=1$$" + key, passhash.ErrParamsOutOfRange},
		{"empty key", "$argon2id$v=19$m=256,t=2,p=1$" + salt + "$", passhash.ErrParamsOutOfRange},

		{"memory above the ceiling", phc("m=1048577,t=1,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"memory at the uint32 limit", phc("m=4294967295,t=1,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"memory past uint32", phc("m=4294967296,t=1,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"memory past uint64", phc("m=99999999999999999999999,t=1,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"zero memory", phc("m=0,t=1,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"memory below eight per lane", phc("m=15,t=1,p=2", 16, 32), passhash.ErrParamsOutOfRange},
		{"zero time", phc("m=64,t=0,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"time above the ceiling", phc("m=64,t=17,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"time past uint32", phc("m=64,t=4294967296,p=1", 16, 32), passhash.ErrParamsOutOfRange},
		{"zero parallelism", phc("m=64,t=1,p=0", 16, 32), passhash.ErrParamsOutOfRange},
		{"parallelism above the ceiling", phc("m=1024,t=1,p=65", 16, 32), passhash.ErrParamsOutOfRange},
		{"parallelism past uint8", phc("m=4096,t=1,p=256", 16, 32), passhash.ErrParamsOutOfRange},
		{"short salt", phc("m=64,t=1,p=1", 7, 32), passhash.ErrParamsOutOfRange},
		{"long salt", phc("m=64,t=1,p=1", 65, 32), passhash.ErrParamsOutOfRange},
		{"short key", phc("m=64,t=1,p=1", 16, 15), passhash.ErrParamsOutOfRange},
		{"long key", phc("m=64,t=1,p=1", 16, 65), passhash.ErrParamsOutOfRange},
	}

	hasher := newHasher(t, cheap)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, needsRehash, err := hasher.Verify("password", tc.encoded)
			if !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}

			if ok || needsRehash {
				t.Errorf("want nothing reported alongside an error, got ok=%t needsRehash=%t", ok, needsRehash)
			}
		})
	}
}

func TestVerifyEvaluatesTheSmallestAcceptedParameters(t *testing.T) {
	ok, _, err := newHasher(t, cheap).Verify("password", phc("m=8,t=1,p=1", 8, 16))
	if err != nil || ok {
		t.Errorf("want a plain mismatch, got ok=%t err=%v", ok, err)
	}
}

func TestNewWithParamsRefusesWhatVerifyWouldRefuse(t *testing.T) {
	cases := map[string]func(*passhash.Params){
		"zero memory":        func(p *passhash.Params) { p.Memory = 0 },
		"memory per lane":    func(p *passhash.Params) { p.Memory, p.Parallelism = 31, 4 },
		"too much memory":    func(p *passhash.Params) { p.Memory = 1<<20 + 1 },
		"zero time":          func(p *passhash.Params) { p.Time = 0 },
		"too much time":      func(p *passhash.Params) { p.Time = 17 },
		"zero parallelism":   func(p *passhash.Params) { p.Parallelism = 0 },
		"too many lanes":     func(p *passhash.Params) { p.Memory, p.Parallelism = 1024, 65 },
		"short salt":         func(p *passhash.Params) { p.SaltLength = 7 },
		"long salt":          func(p *passhash.Params) { p.SaltLength = 65 },
		"short key":          func(p *passhash.Params) { p.KeyLength = 15 },
		"long key":           func(p *passhash.Params) { p.KeyLength = 65 },
		"the zero value":     func(p *passhash.Params) { *p = passhash.Params{} },
		"zero salt and key":  func(p *passhash.Params) { p.SaltLength, p.KeyLength = 0, 0 },
		"parallelism at max": func(p *passhash.Params) { p.Parallelism = 255 },
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			params := cheap
			change(&params)

			if _, err := passhash.NewWithParams(params); !errors.Is(err, passhash.ErrParamsOutOfRange) {
				t.Errorf("want ErrParamsOutOfRange, got %v", err)
			}
		})
	}
}

func TestProductionParameters(t *testing.T) {
	hasher := passhash.New()

	started := time.Now()
	encoded := mustHash(t, hasher, "correct horse battery staple")
	t.Logf("one production hash took %s", time.Since(started))

	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("want the production parameters, got %q", encoded)
	}

	// The column that stores it is a varchar(255).
	if len(encoded) != 97 {
		t.Errorf("want 97 characters, got %d in %q", len(encoded), encoded)
	}

	ok, needsRehash, err := hasher.Verify("correct horse battery staple", encoded)
	if err != nil || !ok || needsRehash {
		t.Errorf("want a match with no rehash, got ok=%t needsRehash=%t err=%v", ok, needsRehash, err)
	}

	ok, needsRehash, err = newHasher(t, cheap).Verify("correct horse battery staple", encoded)
	if err != nil || !ok || !needsRehash {
		t.Errorf("want another hasher to match and ask for a rehash, got ok=%t needsRehash=%t err=%v",
			ok, needsRehash, err)
	}
}

func FuzzVerify(f *testing.F) {
	// The production ceilings would let one generated input ask for a gigabyte.
	hasher, err := passhash.NewWithCeilings(cheap, 256, 2)
	if err != nil {
		f.Fatalf("building the hasher: %v", err)
	}

	valid, err := hasher.Hash("password")
	if err != nil {
		f.Fatalf("hashing: %v", err)
	}

	seeds := []string{
		valid,
		referenceVectors[0].encoded,
		referenceVectors[1].encoded,
		"",
		"$",
		"$$$$$",
		"$argon2id$v=19$m=0,t=0,p=0$$",
		"$argon2id$v=19$m=8,t=1,p=1$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=4294967295,t=4294967295,p=255$c29tZXNhbHQ$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=256,t=2,p=32$c29tZXNhbHQ$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2i$v=19$m=256,t=2,p=1$c29tZXNhbHQ$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=16$m=256,t=2,p=1$c29tZXNhbHQ$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$t=2,m=256,p=1$c29tZXNhbHQ=$AAAA\n",
		"$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
	}

	for _, seed := range seeds {
		f.Add("password", seed)
	}

	f.Fuzz(func(t *testing.T, password, encoded string) {
		ok, needsRehash, err := hasher.Verify(password, encoded)

		if err != nil && (ok || needsRehash) {
			t.Errorf("want nothing reported alongside an error, got ok=%t needsRehash=%t err=%v", ok, needsRehash, err)
		}

		if needsRehash && !ok {
			t.Errorf("want a rehash only for a match, got one for %q", encoded)
		}
	})
}
