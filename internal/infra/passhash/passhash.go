// Package passhash hashes passwords with argon2id and encodes them as PHC
// strings.
package passhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrMalformed        = errors.New("passhash: the encoded hash is malformed")
	ErrUnsupported      = errors.New("passhash: the encoded hash is not argon2id version 19")
	ErrParamsOutOfRange = errors.New("passhash: the parameters are out of range")
)

const (
	algorithm = "argon2id"
	version   = "v=19"
)

// A stored row is read back as instructions for how much to compute, and on
// premises the customer's DBA can write that row: these cap what it can ask for.
const (
	maxMemory      = 1 << 20 // KiB
	maxTime        = 16
	maxParallelism = 64
	minSaltLength  = 8
	maxSaltLength  = 64
	minKeyLength   = 16
	maxKeyLength   = 64
)

// Params are the argon2id cost parameters. Memory is in KiB, lengths in bytes.
type Params struct {
	Memory      uint32
	Time        uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

type Hasher struct {
	params    Params
	maxMemory uint32
	maxTime   uint32
}

// New uses RFC 9106's second recommended option.
func New() *Hasher {
	return &Hasher{
		params:    Params{Memory: 64 * 1024, Time: 3, Parallelism: 4, SaltLength: 16, KeyLength: 32},
		maxMemory: maxMemory,
		maxTime:   maxTime,
	}
}

// NewWithParams exists for tests, which cannot afford 64 MiB per case.
func NewWithParams(params Params) (*Hasher, error) {
	return newWithCeilings(params, maxMemory, maxTime)
}

func newWithCeilings(params Params, memory, time uint32) (*Hasher, error) {
	built := &Hasher{params: params, maxMemory: memory, maxTime: time}

	if !built.inRange(params) {
		return nil, ErrParamsOutOfRange
	}

	return built, nil
}

// The lower bounds are argon2's own: argon2.IDKey panics on a zero time or
// parallelism.
func (h *Hasher) inRange(params Params) bool {
	return params.Parallelism >= 1 && params.Parallelism <= maxParallelism &&
		params.Time >= 1 && params.Time <= h.maxTime &&
		params.Memory >= 8*uint32(params.Parallelism) && params.Memory <= h.maxMemory &&
		params.SaltLength >= minSaltLength && params.SaltLength <= maxSaltLength &&
		params.KeyLength >= minKeyLength && params.KeyLength <= maxKeyLength
}

// Hash returns the PHC string for a password.
func (h *Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("passhash: reading a salt: %w", err)
	}

	return encode(h.params, salt, derive(password, salt, h.params)), nil
}

// Verify reports whether password matches encoded. A wrong password is not an
// error: an error means encoded could not be evaluated at all. needsRehash is
// true when it matched and encoded was produced with parameters other than this
// hasher's, weaker or stronger.
func (h *Hasher) Verify(password, encoded string) (ok bool, needsRehash bool, err error) {
	stored, salt, key, err := decode(encoded)
	if err != nil {
		return false, false, err
	}

	if !h.inRange(stored) {
		return false, false, ErrParamsOutOfRange
	}

	if subtle.ConstantTimeCompare(derive(password, salt, stored), key) != 1 {
		return false, false, nil
	}

	return true, stored != h.params, nil
}

func derive(password string, salt []byte, params Params) []byte {
	return argon2.IDKey([]byte(password), salt, params.Time, params.Memory, params.Parallelism, params.KeyLength)
}

func encode(params Params, salt, key []byte) string {
	return fmt.Sprintf(
		"$%s$%s$m=%d,t=%d,p=%d$%s$%s",
		algorithm, version, params.Memory, params.Time, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}

func decode(encoded string) (Params, []byte, []byte, error) {
	fields := strings.Split(encoded, "$")
	if len(fields) < 2 || fields[0] != "" || fields[1] == "" {
		return Params{}, nil, nil, ErrMalformed
	}

	// Before the field count, so another scheme's hash is reported as what it is.
	if fields[1] != algorithm {
		return Params{}, nil, nil, ErrUnsupported
	}

	if len(fields) != 6 || !strings.HasPrefix(fields[2], "v=") {
		return Params{}, nil, nil, ErrMalformed
	}

	if fields[2] != version {
		return Params{}, nil, nil, ErrUnsupported
	}

	params, err := decodeCosts(fields[3])
	if err != nil {
		return Params{}, nil, nil, err
	}

	salt, err := decodeBase64(fields[4])
	if err != nil {
		return Params{}, nil, nil, err
	}

	key, err := decodeBase64(fields[5])
	if err != nil {
		return Params{}, nil, nil, err
	}

	// #nosec G115 -- a string that fits in memory has a length far below 2^32.
	params.SaltLength, params.KeyLength = uint32(len(salt)), uint32(len(key))

	return params, salt, key, nil
}

func decodeCosts(field string) (Params, error) {
	costs := strings.Split(field, ",")
	if len(costs) != 3 {
		return Params{}, ErrMalformed
	}

	memory, err := decodeDecimal(costs[0], "m=", 32)
	if err != nil {
		return Params{}, err
	}

	time, err := decodeDecimal(costs[1], "t=", 32)
	if err != nil {
		return Params{}, err
	}

	parallelism, err := decodeDecimal(costs[2], "p=", 8)
	if err != nil {
		return Params{}, err
	}

	// #nosec G115 -- decodeDecimal already bounded each value to its width.
	return Params{Memory: uint32(memory), Time: uint32(time), Parallelism: uint8(parallelism)}, nil
}

func decodeDecimal(cost, prefix string, bits int) (uint64, error) {
	digits, found := strings.CutPrefix(cost, prefix)

	// ParseUint refuses signs and separators but takes leading zeros, which the
	// PHC format forbids.
	if !found || (len(digits) > 1 && digits[0] == '0') {
		return 0, ErrMalformed
	}

	value, err := strconv.ParseUint(digits, 10, bits)
	if errors.Is(err, strconv.ErrRange) {
		return 0, ErrParamsOutOfRange
	}

	if err != nil {
		return 0, ErrMalformed
	}

	return value, nil
}

func decodeBase64(field string) ([]byte, error) {
	// The decoder skips CR and LF, so a trailing newline would otherwise pass.
	if strings.ContainsAny(field, "\r\n") {
		return nil, ErrMalformed
	}

	// Strict refuses nonzero trailing bits, leaving one spelling per value.
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(field)
	if err != nil {
		return nil, ErrMalformed
	}

	return decoded, nil
}
