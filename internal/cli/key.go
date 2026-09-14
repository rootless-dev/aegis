package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/infra/keygen"
	"github.com/rootless-dev/aegis/internal/repository"
	"github.com/rootless-dev/aegis/internal/service"
)

func dispatchKey(args []string) ([]string, Runner, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("aegisd: key needs a verb\n\n%s", usage)
	}

	switch args[0] {
	case "list":
		slug, rest, err := requireArgument(args[1:], "key list", "a realm")
		if err != nil {
			return nil, nil, err
		}

		run := func(cfg *configs.Application) int { return runKeyList(cfg, slug) }

		return withoutStrayTokens(rest, run, "key list")
	case "rotate":
		return dispatchKeyRotate(args[1:])
	case "disable":
		return dispatchKeyTransition(args[1:], "key disable", runKeyDisable)
	case "enable":
		return dispatchKeyTransition(args[1:], "key enable", runKeyEnable)
	case "rewrap":
		return withoutStrayTokens(args[1:], runKeyRewrap, "key rewrap")
	default:
		return nil, nil, fmt.Errorf("aegisd: unknown key verb %q\n\n%s", args[0], usage)
	}
}

func dispatchKeyRotate(args []string) ([]string, Runner, error) {
	slug, rest, err := requireArgument(args, "key rotate", "a realm")
	if err != nil {
		return nil, nil, err
	}

	raw, rest, err := requireArgument(rest, "key rotate", "an algorithm")
	if err != nil {
		return nil, nil, err
	}

	// Parsed here, not in the runner: a typo must fail before a database is opened.
	algorithm, err := key.ParseAlgorithm(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("aegisd: %w\n\n%s", err, usage)
	}

	run := func(cfg *configs.Application) int { return runKeyRotate(cfg, slug, algorithm) }

	return withoutStrayTokens(rest, run, "key rotate")
}

func dispatchKeyTransition(
	args []string, command string, run func(*configs.Application, string, string) int,
) ([]string, Runner, error) {
	slug, rest, err := requireArgument(args, command, "a realm")
	if err != nil {
		return nil, nil, err
	}

	kid, rest, err := requireArgument(rest, command, "a kid")
	if err != nil {
		return nil, nil, err
	}

	wrapped := func(cfg *configs.Application) int { return run(cfg, slug, kid) }

	return withoutStrayTokens(rest, wrapped, command)
}

// A leading dash means the argument is missing and the flags have started.
func requireArgument(args []string, command, what string) (string, []string, error) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return "", nil, fmt.Errorf("aegisd: %s needs %s\n\n%s", command, what, usage)
	}

	return args[0], args[1:], nil
}

func keyServices(cfg *configs.Application) (*service.RealmService, *service.KeyService, *database.DB, error) {
	db, _, err := open(cfg)
	if err != nil {
		return nil, nil, nil, err
	}

	// The same construction the server uses, so a subcommand cannot end up with
	// a different sealer or a quieter log than the process that serves.
	logger := log.DefaultLogger

	keeper, err := application.NewSealer(cfg, &logger)
	if err != nil {
		return nil, nil, nil, err
	}

	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return nil, nil, nil, err
	}

	store := repository.NewStore(db.Gorm)
	keys := service.NewKeyService(store, keeper, keygen.New())

	return service.NewRealmService(store, publicURL, keys), keys, db, nil
}

func runKeyList(cfg *configs.Application, slug string) int {
	realms, keys, db, err := keyServices(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	ctx := context.Background()

	found, err := realms.FindBySlug(ctx, slug)
	if err != nil {
		return fail(realmError(slug, err))
	}

	// Every key, disabled ones included: enabling one means seeing it first.
	listed, err := keys.List(ctx, found.ID())
	if err != nil {
		return fail(err)
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	fmt.Fprintln(writer, "KID\tALGORITHM\tSTATUS\tCREATED")

	for _, current := range listed {
		fmt.Fprintf(
			writer, "%s\t%s\t%s\t%s\n",
			current.KID(), current.Algorithm(), current.Status(),
			current.CreatedAt().Format("2006-01-02 15:04:05"),
		)
	}

	if err := writer.Flush(); err != nil {
		return fail(err)
	}

	return 0
}

func runKeyRotate(cfg *configs.Application, slug string, algorithm key.Algorithm) int {
	realms, keys, db, err := keyServices(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	ctx := context.Background()

	found, err := realms.FindBySlug(ctx, slug)
	if err != nil {
		return fail(realmError(slug, err))
	}

	created, err := keys.Rotate(ctx, found.ID(), algorithm)
	if err != nil {
		return fail(err)
	}

	fmt.Printf(
		"rotated %s of realm %s: %s is now active. The previous key is passive and still published, "+
			"so tokens it signed stay verifiable until they expire.\n",
		algorithm, slug, created.KID(),
	)

	return 0
}

func runKeyDisable(cfg *configs.Application, slug, kid string) int {
	advice := fmt.Sprintf(
		"Only a passive key can be disabled: run `aegisd key rotate %s <alg>` first, "+
			"which makes a new key active and this one passive", slug,
	)

	return runKeyTransition(cfg, slug, kid, "disabled", advice, func(
		ctx context.Context, keys *service.KeyService, realmID uuid.UUID,
	) error {
		return keys.Disable(ctx, realmID, kid)
	})
}

func runKeyEnable(cfg *configs.Application, slug, kid string) int {
	advice := "Only a disabled key can be enabled; this one is already in the JWKS"

	return runKeyTransition(cfg, slug, kid, "enabled as passive", advice, func(
		ctx context.Context, keys *service.KeyService, realmID uuid.UUID,
	) error {
		return keys.Enable(ctx, realmID, kid)
	})
}

// advice is per verb: the guidance for a refused transition is the one thing
// these two verbs do not share.
func runKeyTransition(
	cfg *configs.Application, slug, kid, outcome, advice string,
	apply func(context.Context, *service.KeyService, uuid.UUID) error,
) int {
	realms, keys, db, err := keyServices(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	ctx := context.Background()

	found, err := realms.FindBySlug(ctx, slug)
	if err != nil {
		return fail(realmError(slug, err))
	}

	if err := apply(ctx, keys, found.ID()); err != nil {
		if errors.Is(err, key.ErrInvalidTransition) {
			return fail(fmt.Errorf("%w. %s", err, advice))
		}

		return fail(err)
	}

	fmt.Printf("%s of realm %s is now %s\n", kid, slug, outcome)

	return 0
}

func runKeyRewrap(cfg *configs.Application) int {
	_, keys, db, err := keyServices(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	moved, err := keys.Rewrap(context.Background(), 0)
	if err != nil {
		// Reported even on failure: a rewrap is resumable, so an operator who
		// knows how far it got can rerun it instead of restoring a backup.
		fmt.Printf("rewrapped %d keys before failing\n", moved)

		return fail(err)
	}

	fmt.Printf("rewrapped %d keys; every key is now sealed under the current master key\n", moved)

	return 0
}

func realmError(slug string, err error) error {
	if errors.Is(err, realm.ErrNotFound) {
		// No prefix: fail is what writes "aegisd:" for anything a runner returns.
		return fmt.Errorf("no realm named %q", slug)
	}

	return err
}
