// Package audit records who changed what, for every sensitive administrative action.
package audit

import (
	"context"
	"net/netip"

	"github.com/jackc/pgx/v5/pgconn"

	"bm-monitor/api/internal/db"
)

// Execer is satisfied by *pgxpool.Pool and pgx.Tx, so an audit event can be
// written in the same transaction as the change it describes.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Event struct {
	ActorID    string // empty for system actions
	Action     string // e.g. server.create, pricing.change
	TargetType string
	TargetID   string
	Before     any // marshalled to jsonb; nil stores NULL
	After      any
	IP         netip.Addr
	RequestID  string
}

func Record(ctx context.Context, x Execer, e Event) error {
	before, err := db.JSONB(e.Before)
	if err != nil {
		return err
	}
	after, err := db.JSONB(e.After)
	if err != nil {
		return err
	}
	_, err = x.Exec(ctx, `
		INSERT INTO audit_events (actor_id, action, target_type, target_id, before, after, ip, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		nullIfEmpty(e.ActorID), e.Action, e.TargetType, nullIfEmpty(e.TargetID), before, after, ipOrNil(e.IP), nullIfEmpty(e.RequestID))
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ipOrNil(ip netip.Addr) any {
	if !ip.IsValid() {
		return nil
	}
	return ip.String()
}
