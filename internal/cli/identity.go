package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/infra/envtools"
	"github.com/rootless-dev/aegis/internal/repository"
	"github.com/rootless-dev/aegis/internal/service"
)

const (
	identityCreate      = "identity create"
	identitySetPassword = "identity set-password"
	identityList        = "identity list"
	identityDisable     = "identity disable"
	identityEnable      = "identity enable"
)

// An environment variable rather than an argument: an argument sits in `ps`
// and in the shell history.
const passwordVariable = "AEGIS_IDENTITY_PASSWORD"

const argLogin = "a login"

func dispatchIdentity(args []string) ([]string, Runner, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("aegisd: identity needs a verb\n\n%s", usage)
	}

	switch args[0] {
	case "create":
		return dispatchIdentityCreate(args[1:])
	case "set-password":
		return dispatchIdentitySetPassword(args[1:])
	case "list":
		slug, rest, err := requireArgument(args[1:], identityList, argRealm)
		if err != nil {
			return nil, nil, err
		}

		run := func(cfg *configs.Application) int { return runIdentityList(cfg, slug) }

		return withoutStrayTokens(rest, run, identityList)
	case "disable":
		return dispatchIdentityStatus(args[1:], identityDisable, identity.StatusDisabled)
	case "enable":
		return dispatchIdentityStatus(args[1:], identityEnable, identity.StatusActive)
	default:
		return nil, nil, fmt.Errorf("aegisd: unknown identity verb %q\n\n%s", args[0], usage)
	}
}

func dispatchIdentityCreate(args []string) ([]string, Runner, error) {
	temporary, args, err := takeTemporary(args)
	if err != nil {
		return nil, nil, err
	}

	slug, rest, err := requireArgument(args, identityCreate, argRealm)
	if err != nil {
		return nil, nil, err
	}

	email, rest, err := requireArgument(rest, identityCreate, "an email")
	if err != nil {
		return nil, nil, err
	}

	username := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		username, rest = rest[0], rest[1:]
	}

	// Before the environment is read, so a mistyped command line is reported as
	// one.
	if _, _, err := withoutStrayTokens(rest, nil, identityCreate); err != nil {
		return nil, nil, err
	}

	password, supplied, err := lookupPassword()
	if err != nil {
		return nil, nil, err
	}

	if temporary && !supplied {
		return nil, nil, fmt.Errorf("aegisd: --temporary describes a password, and none was supplied. %s\n", passwordAdvice)
	}

	run := func(cfg *configs.Application) int {
		return runIdentityCreate(cfg, slug, email, username, password, supplied, temporary)
	}

	return rest, run, nil
}

func dispatchIdentitySetPassword(args []string) ([]string, Runner, error) {
	temporary, args, err := takeTemporary(args)
	if err != nil {
		return nil, nil, err
	}

	slug, rest, err := requireArgument(args, identitySetPassword, argRealm)
	if err != nil {
		return nil, nil, err
	}

	login, rest, err := requireArgument(rest, identitySetPassword, argLogin)
	if err != nil {
		return nil, nil, err
	}

	if _, _, err := withoutStrayTokens(rest, nil, identitySetPassword); err != nil {
		return nil, nil, err
	}

	password, supplied, err := lookupPassword()
	if err != nil {
		return nil, nil, err
	}

	if !supplied {
		return nil, nil, fmt.Errorf("aegisd: %s needs a password. %s\n", identitySetPassword, passwordAdvice)
	}

	run := func(cfg *configs.Application) int {
		return runIdentitySetPassword(cfg, slug, login, password, temporary)
	}

	return rest, run, nil
}

func dispatchIdentityStatus(args []string, command string, status identity.Status) ([]string, Runner, error) {
	slug, rest, err := requireArgument(args, command, argRealm)
	if err != nil {
		return nil, nil, err
	}

	login, rest, err := requireArgument(rest, command, argLogin)
	if err != nil {
		return nil, nil, err
	}

	run := func(cfg *configs.Application) int { return runIdentityStatus(cfg, slug, login, status) }

	return withoutStrayTokens(rest, run, command)
}

// takeTemporary removes --temporary wherever it sits. It cannot be left for the
// configuration builder, whose flag set does not know it, and requireArgument
// reads a leading dash as "the positional arguments are over".
func takeTemporary(args []string) (bool, []string, error) {
	temporary := false
	rest := make([]string, 0, len(args))

	for _, arg := range args {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")

		if !strings.HasPrefix(arg, "-") || name != "temporary" {
			rest = append(rest, arg)

			continue
		}

		temporary = true

		if hasValue {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return false, nil, fmt.Errorf("aegisd: --temporary takes true or false, got %q\n\n%s", value, usage)
			}

			temporary = parsed
		}
	}

	return temporary, rest, nil
}

const passwordAdvice = "Set " + passwordVariable + ", or " + passwordVariable +
	"_FILE pointing at a file that holds it; a password is never read from the command line"

func lookupPassword() (string, bool, error) {
	password, supplied, err := envtools.LookupSecret(passwordVariable)
	if err != nil {
		return "", false, fmt.Errorf("aegisd: %w\n", err)
	}

	return password, supplied, nil
}

// realmScope is what a verb acting inside one realm works with.
type realmScope struct {
	realm       *realm.Realm
	identities  *service.IdentityService
	credentials *service.CredentialService
	sessions    *service.SessionService
}

// withRealm owns what every verb repeats: the services, the realm lookup and
// the exit code. apply returns an error already worded for an operator.
func withRealm(cfg *configs.Application, slug string, apply func(context.Context, realmScope) error) int {
	// The realm service cannot be built without the key service, hence the
	// sealer. It costs nothing: no configuration validates without a master key.
	realms, keys, db, err := keyServices(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	store := repository.NewStore(db.Gorm)
	identities, credentials := application.NewIdentityServices(store)

	ctx := context.Background()

	found, err := realms.Resolve(ctx, slug)
	if err != nil {
		return fail(identityError(slug, "", err))
	}

	scope := realmScope{
		realm:       found,
		identities:  identities,
		credentials: credentials,
		sessions:    service.NewSessionService(store, credentials, keys),
	}

	if err := apply(ctx, scope); err != nil {
		return fail(err)
	}

	return 0
}

func withIdentities(
	cfg *configs.Application, slug string,
	apply func(context.Context, uuid.UUID, *service.IdentityService, *service.CredentialService) error,
) int {
	return withRealm(cfg, slug, func(ctx context.Context, in realmScope) error {
		return apply(ctx, in.realm.ID(), in.identities, in.credentials)
	})
}

func runIdentityCreate(
	cfg *configs.Application, slug, email, username, password string, withPassword, temporary bool,
) int {
	return withIdentities(cfg, slug, func(
		ctx context.Context, realmID uuid.UUID, identities *service.IdentityService, _ *service.CredentialService,
	) error {
		var (
			created *identity.Identity
			err     error
		)

		if withPassword {
			created, err = identities.CreateWithPassword(ctx, realmID, email, username, password, temporary)
		} else {
			created, err = identities.Create(ctx, realmID, email, username)
		}

		if err != nil {
			return createError(slug, email, username, err)
		}

		fmt.Printf("created identity %s in realm %s\n", created.ID(), slug)
		fmt.Printf("  email:    %s\n", created.Email())
		fmt.Printf("  username: %s\n", orDash(created.Username()))

		switch {
		case !withPassword:
			fmt.Printf(
				"  password: not set; it cannot authenticate until `aegisd identity set-password %s %s` gives it one\n",
				slug, created.Email(),
			)
		case temporary:
			fmt.Println("  password: set, temporary: it has to be changed at the next login")
		default:
			fmt.Println("  password: set")
		}

		return nil
	})
}

func runIdentitySetPassword(cfg *configs.Application, slug, login, password string, temporary bool) int {
	return withIdentities(cfg, slug, func(
		ctx context.Context, realmID uuid.UUID,
		identities *service.IdentityService, credentials *service.CredentialService,
	) error {
		found, err := identities.FindByLogin(ctx, realmID, login)
		if err != nil {
			return identityError(slug, login, err)
		}

		if err := credentials.SetPassword(ctx, realmID, found.ID(), password, temporary); err != nil {
			return identityError(slug, login, err)
		}

		if temporary {
			fmt.Printf("password of %s in realm %s is set; it has to be changed at the next login\n", login, slug)

			return nil
		}

		fmt.Printf("password of %s in realm %s is set\n", login, slug)

		return nil
	})
}

func runIdentityList(cfg *configs.Application, slug string) int {
	return withIdentities(cfg, slug, func(
		ctx context.Context, realmID uuid.UUID, identities *service.IdentityService, _ *service.CredentialService,
	) error {
		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		fmt.Fprintln(writer, "ID\tEMAIL\tUSERNAME\tSTATUS\tCREATED")

		query := service.IdentityQuery{}

		for {
			page, err := identities.List(ctx, realmID, query)
			if err != nil {
				return err
			}

			if len(page) == 0 {
				break
			}

			for _, current := range page {
				fmt.Fprintf(
					writer, "%s\t%s\t%s\t%s\t%s\n",
					current.ID(), current.Email(), orDash(current.Username()), current.Status(),
					current.CreatedAt().Format("2006-01-02 15:04:05"),
				)
			}

			query.After = page[len(page)-1].ID()
		}

		return writer.Flush()
	})
}

func runIdentityStatus(cfg *configs.Application, slug, login string, status identity.Status) int {
	return withIdentities(cfg, slug, func(
		ctx context.Context, realmID uuid.UUID, identities *service.IdentityService, _ *service.CredentialService,
	) error {
		found, err := identities.FindByLogin(ctx, realmID, login)
		if err != nil {
			return identityError(slug, login, err)
		}

		if err := identities.SetStatus(ctx, realmID, found.ID(), status); err != nil {
			return identityError(slug, login, err)
		}

		fmt.Printf("%s of realm %s is now %s\n", login, slug, status)

		return nil
	})
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}

// No "aegisd:" prefix on any of these: fail writes it.
func identityError(slug, login string, err error) error {
	switch {
	case errors.Is(err, realm.ErrNotFound):
		return fmt.Errorf("no realm named %q", slug)
	case errors.Is(err, realm.ErrNotAvailable):
		return fmt.Errorf("realm %q is archived", slug)
	case errors.Is(err, identity.ErrNotFound):
		return fmt.Errorf("no identity %q in realm %q", login, slug)
	case errors.Is(err, credential.ErrPasswordInvalid):
		return errors.New("the password is not acceptable: it has to be between 1 and 1024 bytes long")
	default:
		return err
	}
}

func createError(slug, email, username string, err error) error {
	switch {
	case errors.Is(err, identity.ErrEmailTaken):
		return fmt.Errorf("the email %q is already in use in realm %q", email, slug)
	case errors.Is(err, identity.ErrUsernameTaken):
		return fmt.Errorf("the username %q is already in use in realm %q", username, slug)
	case errors.Is(err, identity.ErrEmailInvalid):
		return fmt.Errorf("%q is not a well formed email", email)
	case errors.Is(err, identity.ErrUsernameInvalid):
		return fmt.Errorf("%q is not a well formed username", username)
	default:
		return identityError(slug, email, err)
	}
}
