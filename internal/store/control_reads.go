package store

import (
	"context"
	"database/sql"
	"net/netip"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func (p *ControlPostgres) ListRouteViews(ctx context.Context, accountID string, query domain.RouteQuery) ([]domain.RouteView, error) {
	status := domain.RouteActive
	if query.Deleted {
		status = domain.RouteDeleted
	}
	return p.readControlRouteViews(ctx, accountID, `status=$2 ORDER BY created_at DESC, id`, status)
}

func (p *ControlPostgres) GetRouteView(ctx context.Context, accountID, routeID string) (domain.RouteView, error) {
	views, err := p.readControlRouteViews(ctx, accountID, `id=$2`, routeID)
	if err != nil {
		return domain.RouteView{}, err
	}
	if len(views) == 0 {
		return domain.RouteView{}, domain.ErrRouteNotFound
	}
	return views[0], nil
}

func (p *ControlPostgres) readControlRouteViews(ctx context.Context, accountID, predicate string, argument any) ([]domain.RouteView, error) {
	// Route metadata and its active run must belong to the same read-only snapshot.
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+controlRouteColumns+` FROM tunnel_routes WHERE account_id=$1 AND `+predicate, accountID, argument)
	if err != nil {
		return nil, err
	}
	views := make([]domain.RouteView, 0)
	ids := make([]string, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		route, err := scanControlRoute(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		indexes[route.ID] = len(views)
		ids = append(ids, route.ID)
		views = append(views, domain.RouteView{Route: route})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) != 0 {
		runs, err := tx.QueryContext(ctx, `SELECT `+controlRunColumns+` FROM tunnel_runs
			WHERE route_id=ANY($2::text[]) AND route_id IN (SELECT id FROM tunnel_routes WHERE account_id=$1)
			AND status IN ('starting','online','stopping')`, accountID, ids)
		if err != nil {
			return nil, err
		}
		for runs.Next() {
			run, err := scanControlRun(runs)
			if err != nil {
				runs.Close()
				return nil, err
			}
			views[indexes[run.RouteID]].CurrentRun = &run
		}
		if err := runs.Err(); err != nil {
			runs.Close()
			return nil, err
		}
		if err := runs.Close(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return views, nil
}

func (p *ControlPostgres) IsIPBanned(ctx context.Context, ip netip.Addr, scope string, now time.Time) (bool, error) {
	if !validControlRunIP(ip) || now.IsZero() || (scope != "tunnel_client" && scope != "public_visitor" && scope != "both") {
		return false, domain.ErrInvalidRequest
	}
	var banned bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM network_bans
		WHERE $1::inet <<= network AND (scope=$2 OR scope='both')
		AND (expires_at IS NULL OR expires_at > $3))`, ip.Unmap().String(), scope, now).Scan(&banned)
	return banned, err
}
