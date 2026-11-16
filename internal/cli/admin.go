package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/domain/admin"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

const (
	bootstrapAdmin = "bootstrap-admin"
	adminList      = "admin list"
	adminGrant     = "admin grant"
	adminRevoke    = "admin revoke"
)

// Grants only belong to identities of this realm.
const masterRealm = "master"

const scopeGlobal = "global"

func dispatchAdmin(args []string) ([]string, Runner, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("aegisd: admin needs a verb\n\n%s", usage)
	}

	switch args[0] {
	case "list":
		return withoutStrayTokens(args[1:], runAdminList, adminList)
	case "grant":
		return dispatchAdminGrant(args[1:], adminGrant, runAdminGrant)
	case "revoke":
		return dispatchAdminGrant(args[1:], adminRevoke, runAdminRevoke)
	default:
		return nil, nil, fmt.Errorf("aegisd: unknown admin verb %q\n\n%s", args[0], usage)
	}
}

func dispatchAdminGrant(
	args []string, command string, run func(*configs.Application, string, string) int,
) ([]string, Runner, error) {
	login, rest, err := requireArgument(args, command, argLogin)
	if err != nil {
		return nil, nil, err
	}

	slug := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		slug, rest = rest[0], rest[1:]
	}

	wrapped := func(cfg *configs.Application) int { return run(cfg, login, slug) }

	return withoutStrayTokens(rest, wrapped, command)
}

func runBootstrapAdmin(cfg *configs.Application) int {
	configured := cfg.Bootstrap.Admin

	if !configured.Configured() {
		return fail(errors.New(
			"no administrator is configured: set AEGIS_BOOTSTRAP_ADMIN_EMAIL and " +
				"AEGIS_BOOTSTRAP_ADMIN_PASSWORD, or AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE pointing at a file that holds it",
		))
	}

	return withServices(cfg, func(ctx context.Context, in services) error {
		// The server seeds the master realm at boot, and this has to work
		// against a database no server has booted on yet.
		if _, err := in.realms.EnsureMaster(ctx, cfg.Profile.IsDev()); err != nil {
			return err
		}

		reset, err := in.admins.ResetAdministrator(ctx, configured.Email, configured.Username, configured.Password)
		if err != nil {
			return createError(masterRealm, configured.Email, configured.Username, err)
		}

		fmt.Printf("administrator %s of realm %s is ready\n", reset.ID(), masterRealm)
		fmt.Printf("  email:    %s\n", reset.Email())
		fmt.Println("  grant:    global: it administers every realm")
		fmt.Println("  password: set, temporary: replace it with `aegisd identity set-password master <email>`")
		fmt.Println("  sessions: any session it had was ended")

		return nil
	})
}

func runAdminList(cfg *configs.Application) int {
	return withServices(cfg, func(ctx context.Context, in services) error {
		master, err := in.realms.FindBySlug(ctx, masterRealm)
		if err != nil {
			return adminError("", "", err)
		}

		grants, err := in.admins.List(ctx)
		if err != nil {
			return err
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		fmt.Fprintln(writer, "EMAIL\tUSERNAME\tSTATUS\tSCOPE\tSINCE")

		for _, held := range grants {
			holder, err := in.identities.FindByID(ctx, master.ID(), held.IdentityID())
			if err != nil {
				return err
			}

			scope := scopeGlobal

			if !held.Global() {
				target, err := in.realms.FindByID(ctx, held.TargetRealmID())
				if err != nil {
					return err
				}

				scope = target.Slug()
			}

			fmt.Fprintf(
				writer, "%s\t%s\t%s\t%s\t%s\n",
				holder.Email(), orDash(holder.Username()), holder.Status(), scope,
				held.CreatedAt().Format(timeLayout),
			)
		}

		return writer.Flush()
	})
}

func runAdminGrant(cfg *configs.Application, login, slug string) int {
	return withAdminTarget(cfg, login, slug, func(
		ctx context.Context, in services, holder *identity.Identity, target uuid.UUID,
	) error {
		var err error

		if target == uuid.Nil {
			_, err = in.admins.GrantGlobal(ctx, holder.ID())
		} else {
			_, err = in.admins.GrantRealm(ctx, holder.ID(), target)
		}

		if errors.Is(err, admin.ErrExists) {
			return fmt.Errorf("%q already administers %s", login, scopeName(slug))
		}

		if err != nil {
			return adminError(login, slug, err)
		}

		fmt.Printf("%s now administers %s\n", holder.Email(), scopeName(slug))

		return nil
	})
}

func runAdminRevoke(cfg *configs.Application, login, slug string) int {
	return withAdminTarget(cfg, login, slug, func(
		ctx context.Context, in services, holder *identity.Identity, target uuid.UUID,
	) error {
		err := in.admins.Revoke(ctx, holder.ID(), target)

		if errors.Is(err, admin.ErrNotFound) {
			return fmt.Errorf("%q does not administer %s", login, scopeName(slug))
		}

		if err != nil {
			return adminError(login, slug, err)
		}

		fmt.Printf("%s no longer administers %s\n", holder.Email(), scopeName(slug))

		return nil
	})
}

// withAdminTarget resolves what grant and revoke share: the holder, by login in
// the master realm, and the target, uuid.Nil when no realm was named.
func withAdminTarget(
	cfg *configs.Application, login, slug string,
	apply func(context.Context, services, *identity.Identity, uuid.UUID) error,
) int {
	return withServices(cfg, func(ctx context.Context, in services) error {
		master, err := in.realms.FindBySlug(ctx, masterRealm)
		if err != nil {
			return adminError(login, "", err)
		}

		holder, err := in.identities.FindByLogin(ctx, master.ID(), login)
		if err != nil {
			return adminError(login, slug, err)
		}

		target := uuid.Nil

		if slug != "" {
			// Not Resolve: administering an unavailable realm is how it comes back.
			found, err := in.realms.FindBySlug(ctx, slug)
			if err != nil {
				return adminError(login, slug, err)
			}

			target = found.ID()
		}

		return apply(ctx, in, holder, target)
	})
}

func scopeName(slug string) string {
	if slug == "" {
		return "every realm"
	}

	return fmt.Sprintf("realm %q", slug)
}

// An empty slug is the master realm's own lookup failing.
func adminError(login, slug string, err error) error {
	switch {
	case errors.Is(err, realm.ErrNotFound) && slug == "":
		return errors.New(
			"the master realm does not exist yet: boot the server once, or run `aegisd bootstrap-admin`",
		)
	case errors.Is(err, realm.ErrNotFound):
		return fmt.Errorf("no realm named %q", slug)
	case errors.Is(err, identity.ErrNotFound):
		return fmt.Errorf("no identity %q in realm %q", login, masterRealm)
	default:
		return err
	}
}
