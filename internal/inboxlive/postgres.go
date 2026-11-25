package inboxlive

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errListenFailed = errors.New("inbox notification subscription failed")

// PostgresConnector reuses an explicitly configured instance database. Copying
// the config prevents retry/session options from mutating the caller's config.
// Errors returned by this adapter contain no DSN, credential or server payload.
func PostgresConnector(configuration *pgx.ConnConfig) Connector {
	return func(ctx context.Context) (Source, error) {
		if configuration == nil {
			return nil, errors.New("inbox database configuration unavailable")
		}
		conn, err := pgx.ConnectConfig(ctx, configuration.Copy())
		if err != nil {
			return nil, errors.New("inbox database connection failed")
		}
		if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Close(closeCtx)
			cancel()
			return nil, errListenFailed
		}
		return &postgresSource{connection: conn}, nil
	}
}

type postgresSource struct{ connection *pgx.Conn }

func (source *postgresSource) Wait(ctx context.Context) ([]byte, error) {
	for {
		notice, err := source.connection.WaitForNotification(ctx)
		if err != nil {
			return nil, errors.New("inbox notification read failed")
		}
		if notice.Channel == Channel {
			return []byte(notice.Payload), nil
		}
	}
}

func (source *postgresSource) Close(ctx context.Context) error {
	if err := source.connection.Close(ctx); err != nil {
		return errors.New("inbox notification connection close failed")
	}
	return nil
}
