package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bm-monitor/api/internal/accessreq"
	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/config"
	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/insights"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

// TargetSyncer is told when the inventory changed (the targets writer).
type TargetSyncer interface{ Trigger() }

type noopSyncer struct{}

func (noopSyncer) Trigger() {}

// Deps are the API's collaborators.
type Deps struct {
	Config  *config.Config
	Pool    *pgxpool.Pool
	Auth    *auth.Service
	Prom    *metrics.Client
	Targets TargetSyncer
	Log     *slog.Logger
	Access  *accessreq.Service // nil when the SSH access workflow is not configured
}

// API holds the dependencies shared by every handler.
type API struct {
	cfg          *config.Config
	pool         *pgxpool.Pool
	auth         *auth.Service
	log          *slog.Logger
	prom         *metrics.Client
	targets      TargetSyncer
	inv          *inventory.Store
	guard        *inventory.Guard
	costs        *cost.Store
	alerts       *alerts.Store
	senders      *alerts.Senders
	insights     *insights.Service
	access       *accessreq.Service
	publicOrigin string

	loginLimiter    *ipLimiter
	registerLimiter *ipLimiter

	snapMu  sync.Mutex
	snapAt  time.Time
	snap    *metrics.Snapshot
	snapErr error
}

func New(d Deps) *API {
	if d.Targets == nil {
		d.Targets = noopSyncer{}
	}
	inv := inventory.NewStore(d.Pool)
	costs := cost.NewStore(d.Pool, d.Config.BillingTimezone, d.Config.BillingCurrency, d.Log)
	return &API{
		cfg: d.Config, pool: d.Pool, auth: d.Auth, log: d.Log, prom: d.Prom, targets: d.Targets,
		inv: inv,
		guard: &inventory.Guard{
			Allowed: d.Config.AllowedTargetCIDRs, Denied: d.Config.DeniedTargetCIDRs, AllowedPorts: d.Config.AllowedTargetPorts,
			BasicUser: d.Config.NodeExporterUser, BasicPass: d.Config.NodeExporterPassword,
		},
		costs:        costs,
		alerts:       alerts.NewStore(d.Pool),
		senders:      alerts.NewSenders(d.Config),
		insights:     insights.New(d.Pool, d.Prom, inv, costs),
		access:       d.Access,
		publicOrigin: d.Config.PublicURL.Scheme + "://" + d.Config.PublicURL.Host,
		// 5 attempts per IP, then one more every 3 minutes (5 per 15 min sustained).
		loginLimiter: newIPLimiter(3*time.Minute, 5),
		// Endpoint checks make outbound connections: 30 per IP, then one every 20 s.
		registerLimiter: newIPLimiter(20*time.Second, 30),
	}
}

// Costs exposes the cost store for background workers.
func (a *API) Costs() *cost.Store { return a.costs }

// Inventory exposes the inventory store for background workers.
func (a *API) Inventory() *inventory.Store { return a.inv }

// snapshot returns the fleet metrics snapshot, cached for 10 s so many concurrent
// dashboard requests cost Prometheus one round of queries.
func (a *API) snapshot(ctx context.Context) (*metrics.Snapshot, error) {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	ttl := 10 * time.Second
	if a.snapErr != nil {
		ttl = 3 * time.Second
	}
	if time.Since(a.snapAt) < ttl {
		return a.snap, a.snapErr
	}
	a.snap, a.snapErr = a.prom.Snapshot(ctx)
	a.snapAt = time.Now()
	return a.snap, a.snapErr
}

// Handler builds the router. Middleware order follows the plan (section 9.2):
// request id → log → recover → security headers → session → CSRF → role → rate limit → handler.
func (a *API) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.requestID, a.accessLog, a.recoverer, securityHeaders)

	r.Get("/healthz", a.healthz)
	r.Get("/readyz", a.readyz)

	viewer, operator, admin := requireRole(auth.RoleViewer), requireRole(auth.RoleOperator), requireRole(auth.RoleAdmin)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(timeout(15*time.Second), a.session, a.csrf)

		r.Get("/healthz", a.healthz)
		r.Get("/readyz", a.readyz)

		r.With(a.loginLimiter.middleware).Post("/auth/login", a.login)
		r.Get("/auth/providers", a.authProviders)
		r.With(a.loginLimiter.middleware).Get("/auth/google", a.googleStart)
		r.Get("/auth/google/callback", a.googleCallback)

		r.Group(func(r chi.Router) {
			r.Use(viewer)
			r.Post("/auth/logout", a.logout)
			r.Get("/me", a.me)
			r.Post("/me/password", a.changePassword)

			r.Get("/fleet/summary", a.fleetSummary)

			r.Get("/servers", a.listServers)
			r.Get("/servers/{id}", a.getServer)
			r.Get("/servers/{id}/metrics", a.serverMetrics)
			r.Get("/servers/{id}/pricing", a.serverPricing)
			r.With(operator, a.registerLimiter.middleware).Post("/servers/validate", a.validateServer)
			r.With(operator, a.registerLimiter.middleware).Post("/servers", a.createServer)
			r.With(operator).Patch("/servers/{id}", a.updateServer)
			r.With(operator).Post("/servers/{id}/enable", a.enableServer)
			r.With(operator).Post("/servers/{id}/disable", a.disableServer)
			r.With(admin).Delete("/servers/{id}", a.deleteServer)
			r.With(admin).Put("/servers/{id}/pricing", a.assignPricing)

			r.Get("/groups", a.listGroups)
			r.With(operator).Post("/groups", a.createGroup)
			r.With(operator).Patch("/groups/{id}", a.updateGroup)
			r.With(admin).Delete("/groups/{id}", a.deleteGroup)

			r.Get("/pricing-plans", a.listPlans)
			r.With(admin).Post("/pricing-plans", a.createPlan)
			r.With(admin).Patch("/pricing-plans/{id}", a.updatePlan)
			r.Get("/fixed-costs", a.listFixed)
			r.With(admin).Post("/fixed-costs", a.createFixed)
			r.With(admin).Patch("/fixed-costs/{id}", a.updateFixed)
			r.With(admin).Delete("/fixed-costs/{id}", a.deleteFixed)
			r.Get("/costs/summary", a.costSummary)
			r.Get("/costs/trend", a.costTrend)
			r.With(admin).Post("/costs/import", a.importCosts)

			r.Get("/alerts", a.listAlerts)
			r.With(operator).Post("/alerts/{id}/ack", a.ackAlert)
			r.Get("/alert-rules", a.listRules)
			r.With(admin).Post("/alert-rules", a.createRule)
			r.With(admin).Patch("/alert-rules/{id}", a.updateRule)
			r.With(admin).Delete("/alert-rules/{id}", a.deleteRule)
			r.With(admin).Get("/notification-channels", a.listChannels)
			r.With(admin).Post("/notification-channels", a.createChannel)
			r.With(admin).Patch("/notification-channels/{id}", a.updateChannel)
			r.With(admin).Post("/notification-channels/{id}/test", a.testChannel)

			r.Get("/insights/underutilized", a.underutilized)
			r.Get("/insights/compare", a.compare)

			r.Get("/access/config", a.accessConfig)
			r.Get("/access/requests", a.listAccessRequests)
			r.With(a.registerLimiter.middleware).Post("/access/requests", a.createAccessRequest)
			r.With(admin).Get("/access/grants", a.listGrants)
			r.With(admin).Post("/access/grants/{grantID}/revoke", a.revokeGrant)

			r.Get("/settings", a.getSettings)
			r.With(admin).Put("/settings", a.putSettings)
			r.With(admin).Get("/audit", a.listAudit)
			r.With(admin).Get("/users", a.listUsers)
			r.With(admin).Post("/users", a.createUser)
			r.With(admin).Patch("/users/{id}", a.updateUser)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, &APIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "Method not allowed."})
	})
	return r
}
