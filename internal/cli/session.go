package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

const (
	sessionList      = "session list"
	sessionRevoke    = "session revoke"
	sessionRevokeAll = "session revoke-all"
)

const (
	timeLayout         = "2006-01-02 15:04:05"
	userAgentMaxLength = 40
)

func dispatchSession(args []string) ([]string, Runner, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("aegisd: session needs a verb\n\n%s", usage)
	}

	switch args[0] {
	case "list":
		return dispatchSessionByLogin(args[1:], sessionList, runSessionList)
	case "revoke":
		return dispatchSessionRevoke(args[1:])
	case "revoke-all":
		return dispatchSessionByLogin(args[1:], sessionRevokeAll, runSessionRevokeAll)
	default:
		return nil, nil, fmt.Errorf("aegisd: unknown session verb %q\n\n%s", args[0], usage)
	}
}

func dispatchSessionByLogin(
	args []string, command string, run func(*configs.Application, string, string) int,
) ([]string, Runner, error) {
	slug, rest, err := requireArgument(args, command, argRealm)
	if err != nil {
		return nil, nil, err
	}

	login, rest, err := requireArgument(rest, command, argLogin)
	if err != nil {
		return nil, nil, err
	}

	wrapped := func(cfg *configs.Application) int { return run(cfg, slug, login) }

	return withoutStrayTokens(rest, wrapped, command)
}

func dispatchSessionRevoke(args []string) ([]string, Runner, error) {
	slug, rest, err := requireArgument(args, sessionRevoke, argRealm)
	if err != nil {
		return nil, nil, err
	}

	raw, rest, err := requireArgument(rest, sessionRevoke, "a session id")
	if err != nil {
		return nil, nil, err
	}

	// Parsed here, not in the runner: a typo must fail before a database is opened.
	sessionID, err := uuid.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("aegisd: %q is not a session id\n\n%s", raw, usage)
	}

	run := func(cfg *configs.Application) int { return runSessionRevoke(cfg, slug, sessionID) }

	return withoutStrayTokens(rest, run, sessionRevoke)
}

func runSessionList(cfg *configs.Application, slug, login string) int {
	return withRealm(cfg, slug, func(ctx context.Context, in realmScope) error {
		owner, err := in.identities.FindByLogin(ctx, in.realm.ID(), login)
		if err != nil {
			return identityError(slug, login, err)
		}

		listed, err := in.sessions.ListByIdentity(ctx, in.realm.ID(), owner.ID())
		if err != nil {
			return err
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		fmt.Fprintln(writer, "ID\tSTATUS\tCREATED\tLAST SEEN\tEXPIRES\tIP\tUSER AGENT")

		now := time.Now()
		idleTTL := in.realm.Settings().SessionIdleTTL

		for _, current := range listed {
			fmt.Fprintf(
				writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				current.ID(), sessionStatus(current, now, idleTTL),
				current.CreatedAt().Format(timeLayout), current.LastSeenAt().Format(timeLayout),
				current.ExpiresAt().Format(timeLayout),
				orDash(current.IP()), orDash(shorten(current.UserAgent(), userAgentMaxLength)),
			)
		}

		return writer.Flush()
	})
}

func runSessionRevoke(cfg *configs.Application, slug string, sessionID uuid.UUID) int {
	return withRealm(cfg, slug, func(ctx context.Context, in realmScope) error {
		err := in.sessions.Revoke(ctx, in.realm.ID(), sessionID)

		switch {
		case errors.Is(err, session.ErrNotFound):
			return fmt.Errorf("no session %s in realm %q", sessionID, slug)
		case errors.Is(err, session.ErrEnded):
			return fmt.Errorf("session %s of realm %q has already ended", sessionID, slug)
		case err != nil:
			return err
		}

		fmt.Printf("session %s of realm %s is now revoked\n", sessionID, slug)

		return nil
	})
}

func runSessionRevokeAll(cfg *configs.Application, slug, login string) int {
	return withRealm(cfg, slug, func(ctx context.Context, in realmScope) error {
		owner, err := in.identities.FindByLogin(ctx, in.realm.ID(), login)
		if err != nil {
			return identityError(slug, login, err)
		}

		ended, err := in.sessions.RevokeAllForIdentity(ctx, in.realm.ID(), owner.ID())
		if err != nil {
			return err
		}

		fmt.Printf("revoked %d sessions of %s in realm %s\n", ended, login, slug)

		return nil
	})
}

func sessionStatus(s *session.Session, now time.Time, idleTTL time.Duration) string {
	switch {
	case s.Ended():
		return "ended:" + s.EndReason().String()
	case s.Usable(now, idleTTL) != nil:
		return "expired"
	default:
		return "live"
	}
}

func shorten(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit-3]) + "..."
}
