package configs

import "fmt"

// Profile is how the process was launched. It exists because a few settings
// cannot have the same default in both worlds, TLS above all, and it never
// relaxes a check quietly: what dev makes convenient, prod has to be told.
type Profile string

const (
	ProfileDev  Profile = "dev"
	ProfileProd Profile = "prod"
)

func (p Profile) String() string {
	return string(p)
}

func (p Profile) IsDev() bool {
	return p == ProfileDev
}

func (p Profile) Validate() error {
	switch p {
	case ProfileDev, ProfileProd:
		return nil
	default:
		return fmt.Errorf("application: unsupported profile %q, want %q or %q", p, ProfileDev, ProfileProd)
	}
}
