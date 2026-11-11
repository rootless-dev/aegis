package application

import (
	"context"
	"fmt"
)

// ensureAdministrator runs after the master realm is seeded, which it needs.
// Its budget is its own: hashing the password must not be paid out of the one
// that generated the keys.
func (app *Application) ensureAdministrator() error {
	ctx, cancel := context.WithTimeout(context.Background(), app.cfg.Database.ConnectTimeout)
	defer cancel()

	if err := app.bootstrapAdministrator(ctx); err != nil {
		return fmt.Errorf("application: bootstrapping the administrator: %w", err)
	}

	temporary, err := app.admins.TemporaryAdministrators(ctx)
	if err != nil {
		return fmt.Errorf("application: listing the administrators: %w", err)
	}

	// On every boot: the secret that seeded the password stays in the
	// environment and in backups, and nothing else forces the change.
	for _, holder := range temporary {
		app.logger.Warn().
			Str("email", holder.Email()).
			Msg("this administrator's password is still the temporary one; replace it with " +
				"`aegisd identity set-password master <email>`, without --temporary")
	}

	return nil
}

func (app *Application) bootstrapAdministrator(ctx context.Context) error {
	configured := app.cfg.Bootstrap.Admin

	if !configured.Configured() {
		return app.warnWithoutAdministrator(ctx)
	}

	outcome, err := app.admins.Bootstrap(ctx, configured.Email, configured.Username, configured.Password)
	if err != nil {
		return err
	}

	switch {
	case outcome.Created:
		app.logger.Warn().
			Str("email", outcome.Administrator.Email()).
			Msg("first administrator created with the temporary password from the bootstrap configuration")
	case outcome.Completed:
		app.logger.Warn().
			Str("email", outcome.Administrator.Email()).
			Msg("an existing identity was made the first administrator; its password is now the temporary " +
				"one from the bootstrap configuration and its sessions were ended")
	default:
		app.logger.Info().
			Msg("an administrator already exists; the bootstrap credentials were ignored and can be " +
				"removed from the environment")
	}

	return nil
}

func (app *Application) warnWithoutAdministrator(ctx context.Context) error {
	exists, err := app.admins.HasAdministrator(ctx)
	if err != nil || exists {
		return err
	}

	app.logger.Warn().
		Msg("no administrator able to sign in exists; set AEGIS_BOOTSTRAP_ADMIN_EMAIL and " +
			"AEGIS_BOOTSTRAP_ADMIN_PASSWORD (or AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE) and restart, or run " +
			"`aegisd bootstrap-admin` with them set")

	return nil
}
