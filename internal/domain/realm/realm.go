// Package realm holds the tenancy root. It imports nothing beyond the standard
// library and uuid.
package realm

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 63 is the DNS label limit, so the slug already fits as a subdomain if custom
// domains ever ship. No dot, which makes ".well-known" unrepresentable.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Enforced by the service, not here: the boot seed creates master.
var reservedSlugs = []string{"aegis", "admin", "api", "master"}

const maxDisplayNameLength = 255

// Private fields with accessors: the issuer is immutable by compiler, not by
// convention.
type Realm struct {
	id          uuid.UUID
	slug        string
	displayName string
	issuer      string
	status      Status
	createdAt   time.Time
	updatedAt   time.Time
}

// New generates the identifier, derives the issuer, and stamps both timestamps
// to the same UTC instant.
func New(slug, displayName string, publicBaseURL *url.URL) (*Realm, error) {
	slug = strings.TrimSpace(slug)
	displayName = strings.TrimSpace(displayName)

	if err := validateSlug(slug); err != nil {
		return nil, err
	}

	if err := validateDisplayName(displayName); err != nil {
		return nil, err
	}

	issuer, err := DeriveIssuer(publicBaseURL, slug)
	if err != nil {
		return nil, err
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("realm: generating an identifier: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)

	return &Realm{
		id:          id,
		slug:        slug,
		displayName: displayName,
		issuer:      issuer,
		status:      StatusActive,
		createdAt:   now,
		updatedAt:   now,
	}, nil
}

// Rehydrate is the repository's door back in. It takes the stored issuer as
// given: deriving here would recompute every realm's identity from whatever
// public url this process holds, which is what the stored column prevents.
func Rehydrate(
	id uuid.UUID,
	slug, displayName, issuer string,
	status Status,
	createdAt, updatedAt time.Time,
) (*Realm, error) {
	if id == uuid.Nil {
		return nil, ErrIDInvalid
	}

	if err := validateSlug(slug); err != nil {
		return nil, err
	}

	if err := validateDisplayName(displayName); err != nil {
		return nil, err
	}

	if err := validateIssuer(issuer); err != nil {
		return nil, err
	}

	if _, err := ParseStatus(string(status)); err != nil {
		return nil, err
	}

	return &Realm{
		id:          id,
		slug:        slug,
		displayName: displayName,
		issuer:      issuer,
		status:      status,
		createdAt:   createdAt.UTC(),
		updatedAt:   updatedAt.UTC(),
	}, nil
}

func (r *Realm) ID() uuid.UUID        { return r.id }
func (r *Realm) Slug() string         { return r.slug }
func (r *Realm) DisplayName() string  { return r.displayName }
func (r *Realm) Issuer() string       { return r.issuer }
func (r *Realm) Status() Status       { return r.status }
func (r *Realm) CreatedAt() time.Time { return r.createdAt }
func (r *Realm) UpdatedAt() time.Time { return r.updatedAt }

func (r *Realm) Rename(displayName string) error {
	displayName = strings.TrimSpace(displayName)

	if err := validateDisplayName(displayName); err != nil {
		return err
	}

	r.displayName = displayName
	r.touch()

	return nil
}

func (r *Realm) SetStatus(status Status) error {
	parsed, err := ParseStatus(string(status))
	if err != nil {
		return err
	}

	r.status = parsed
	r.touch()

	return nil
}

// No setter for id, slug, issuer or createdAt. Rewriting an issuer is a
// repository operation, not part of a realm's own behaviour.

func (r *Realm) touch() {
	r.updatedAt = time.Now().UTC().Truncate(time.Microsecond)
}

func IsReservedSlug(slug string) bool {
	return slices.Contains(reservedSlugs, slug)
}

func validateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w: %q", ErrSlugInvalid, slug)
	}

	return nil
}

func validateDisplayName(displayName string) error {
	if strings.TrimSpace(displayName) == "" || len(displayName) > maxDisplayNameLength {
		return fmt.Errorf("%w: %q", ErrDisplayNameInvalid, displayName)
	}

	return nil
}

// DeriveIssuer runs once per realm, at creation, and the result is stored.
// Nothing may call it on the read path — that is the drift the stored column
// exists to prevent.
func DeriveIssuer(base *url.URL, slug string) (string, error) {
	if base == nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("%w: the public base url must be absolute", ErrIssuerInvalid)
	}

	// Clients compare iss byte for byte and build discovery by appending to it,
	// so anything beyond scheme, host and path cannot work.
	if base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("%w: the public base url carries a query or a fragment", ErrIssuerInvalid)
	}

	issuer := strings.TrimSuffix(base.String(), "/") + "/realms/" + slug

	if err := validateIssuer(issuer); err != nil {
		return "", err
	}

	return issuer, nil
}

func validateIssuer(issuer string) error {
	if issuer == "" || len(issuer) > 255 {
		return fmt.Errorf("%w: %q", ErrIssuerInvalid, issuer)
	}

	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: %q", ErrIssuerInvalid, issuer)
	}

	if strings.HasSuffix(issuer, "/") {
		return fmt.Errorf("%w: %q ends in a slash", ErrIssuerInvalid, issuer)
	}

	return nil
}
