// Package app assembles every subsystem into a runnable application and owns
// the background loops (mapper reconcile, gateway discovery, scanner monitor,
// scheduler).
package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/api"
	"github.com/ekko-studio/natpilot/internal/auth"
	"github.com/ekko-studio/natpilot/internal/bus"
	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/mapper"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/scanner"
	"github.com/ekko-studio/natpilot/internal/scheduler"
	"github.com/ekko-studio/natpilot/internal/store"
)

// App is the assembled application.
type App struct {
	API       *api.App
	Store     *store.Store
	Config    *config.Config
	Mapper    *mapper.Mapper
	Scanner   *scanner.Scanner
	Auth      *auth.Service
	Bus       *bus.Bus
	Scheduler *scheduler.Scheduler

	stopLogBridge func()
	startedAt     time.Time
}

// Options configures Build.
type Options struct {
	ConfigPath string
	Flags      map[string]string
	Version    string
	// SkipBackground disables the periodic loops (used by one-shot CLI commands).
	SkipBackground bool
}

// Build opens the database, applies configuration and constructs the app.
func Build(ctx context.Context, opts Options) (*App, error) {
	cfg, err := config.Load(opts.ConfigPath, opts.Flags)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	logging.Init(cfg.Log.Level, cfg.Log.Format)

	st, err := store.Open(cfg.DSN(), cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	if cfg.App.AutoMigrate {
		if err := st.Migrate(); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("migrate database: %w", err)
		}
	}
	// Re-apply persisted UI overrides on top of the file configuration.
	if ov, err := st.AllSettings(); err == nil {
		overrides := map[string]string{}
		for k, v := range ov {
			if strings.HasPrefix(k, "config.") {
				overrides[strings.TrimPrefix(k, "config.")] = v
			}
		}
		if len(overrides) > 0 {
			if rejected := cfg.ApplyOverrides(overrides); len(rejected) > 0 {
				logging.Warn("config", "ignored unknown persisted overrides", "keys", strings.Join(rejected, ","))
			}
		}
	}

	b := bus.New()
	// 每轮读取最新配置：设置页修改保留策略后无需重启即可生效。
	stopBridge := bus.Bridge(b, st, func() bus.Retention {
		c := cfg
		return bus.Retention{
			EventsMax:  firstNonZero(c.Retention.EventsMax, c.Log.MaxEntries),
			EventsDays: firstNonZero(c.Retention.EventsDays, c.Log.Retention),
			ScansMax:   c.Retention.ScansMax,
			ScansDays:  firstNonZero(c.Retention.ScansDays, c.Log.Retention),
		}
	})

	a := &App{
		Store:         st,
		Config:        cfg,
		Bus:           b,
		stopLogBridge: stopBridge,
		startedAt:     time.Now(),
	}

	a.Auth = auth.New(st, cfg)
	if err := a.Auth.EnsureBootstrapUser(); err != nil {
		logging.Warn("auth", "could not create the bootstrap administrator", "error", err.Error())
	}
	// Authentication is opt-in. Binding a routable address without it leaves an
	// unauthenticated admin console on the network, so say it out loud instead of
	// letting the operator discover it later.
	if !cfg.AuthRequired() && !cfg.IsLoopbackHost() {
		logging.Warn("auth",
			"the console is reachable from the network but authentication is disabled; start with --auth (or auth.enabled: true) to require login",
			"bind", cfg.EffectiveBindHost(), "mode", cfg.EffectiveMode())
	}

	a.Mapper = mapper.New(st, cfg, a)
	// Restore the mapping state that survived the previous run, otherwise cleanup
	// and renewal have nothing to work with after a restart.
	a.Mapper.LoadRecords()
	a.Scanner = scanner.New(st, cfg, a)
	a.Scheduler = scheduler.New(st)

	a.registerSchedulerJobs()

	if err := a.seedDefaults(); err != nil {
		logging.Warn("app", "could not seed default data", "error", err.Error())
	}

	a.API = &api.App{
		Store:     st,
		Config:    cfg,
		Mapper:    a.Mapper,
		Scanner:   a.Scanner,
		Auth:      a.Auth,
		Bus:       b,
		Scheduler: a.Scheduler,
		Version:   opts.Version,
		StartedAt: a.startedAt,
	}
	return a, nil
}

// StartedAt reports the process start time.
func (a *App) StartedAt() time.Time { return a.startedAt }

// seedDefaults creates a helpful starting point on an empty database.
func (a *App) seedDefaults() error {
	pols, err := a.Store.ListPolicies()
	if err != nil {
		return err
	}
	if len(pols) == 0 {
		_, err := a.Store.CreatePolicy(&model.Policy{
			Name:        "Common local services",
			Description: "Example default strategy: watch common dev/media ports and tag them. Enable it to let the scanner act automatically.",
			Enabled:     false,
			MatchTarget: "any",
			PortRanges:  "3000-3010,5000,8000-8100,8080,8443,9000,9090,32400",
			Protocol:    "any",
			AutoAction:  model.ActionWatch,
			AutoAdd:     false,
			Tags:        model.StringList{"local"},
			Priority:    10,
		})
		if err != nil {
			return err
		}
		logging.Info("app", "seeded example policy (disabled by default)")
	}
	return nil
}

// Run starts the background loops.
func (a *App) Run(ctx context.Context) {
	if a.Config.Mapper.Enabled {
		a.Mapper.Run(ctx)
	}
	if a.Config.Scanner.AutoMonitorLocal {
		if err := a.Scanner.StartMonitor(ctx); err != nil {
			logging.Warn("scanner", "auto monitor did not start", "error", err.Error())
		}
	}
	a.Scheduler.Start(ctx)
}

// Shutdown stops background work and closes the database.
func (a *App) Shutdown() {
	if a.Mapper != nil {
		a.Mapper.Shutdown()
	}
	if a.Scanner != nil {
		// Cancel in-flight scans too, not just the monitor: otherwise a scan keeps
		// running against a database that is about to be closed.
		a.Scanner.Shutdown()
	}
	if a.Scheduler != nil {
		a.Scheduler.Stop()
	}
	if a.stopLogBridge != nil {
		a.stopLogBridge()
	}
	if a.Store != nil {
		_ = a.Store.Close()
	}
}

// ---- mapper events ----

// Publish implements mapper.Events and scanner.Events.
func (a *App) Publish(category, message string, meta model.JSONMap) {
	// 与 Store.AddEvent 同一约定：关闭日志时既不落库也不实时推送。
	if logging.LevelOf() == "off" {
		return
	}
	level := "info"
	if category == "mapping" && strings.Contains(message, "failed") {
		level = "warn"
	}
	if err := a.Store.AddEvent(level, category, message, meta); err != nil {
		return
	}
	data := map[string]any{"level": level, "category": category, "message": message}
	if meta != nil {
		data["meta"] = meta
	}
	a.Bus.PublishType("log", data)
}

// Changed implements mapper.Events and scanner.Events.
func (a *App) Changed() {
	a.Bus.PublishType("changed", map[string]any{"at": time.Now()})
}

// Progress implements scanner.Events.
func (a *App) Progress(scanID int64, pct int, message string) {
	a.Bus.PublishType("scan-progress", map[string]any{"scan_id": scanID, "progress": pct, "message": message})
}

// ---- scheduler jobs ----

func (a *App) registerSchedulerJobs() {
	a.Scheduler.Register("scan", func(ctx context.Context, sc model.Schedule) (string, error) {
		// The schedule may pin an explicit port list. FilterPorts must be set for it
		// to act as a filter: otherwise a local scan enumerates every listening
		// socket on the machine and the configured ports are ignored.
		spec := strings.TrimSpace(sc.PortsSpec)
		req := scanner.Request{
			Name:        sc.Name,
			TargetKind:  sc.TargetKind,
			TargetRef:   strings.TrimSpace(sc.TargetRef),
			PortsSpec:   spec,
			FilterPorts: spec != "",
			PolicyID:    sc.PolicyID,
			Trigger:     "schedule",
			ApplyPolicy: true,
		}
		if req.PortsSpec == "" {
			req.PortsSpec = a.Config.Scanner.DefaultPorts
		}
		run, err := a.Scanner.Run(ctx, req)
		if err != nil {
			return "error", err
		}
		return fmt.Sprintf("scan #%d finished (%d open)", run.ID, run.Open), nil
	})
	a.Scheduler.Register("reconcile", func(ctx context.Context, sc model.Schedule) (string, error) {
		sum, err := a.Mapper.Reconcile(ctx)
		if err != nil {
			return "error", err
		}
		return "reconciled: " + sum.String(), nil
	})
	a.Scheduler.Register("cleanup", func(ctx context.Context, sc model.Schedule) (string, error) {
		rules, err := a.Store.AllRules()
		if err != nil {
			return "error", err
		}
		removed := 0
		for _, rule := range rules {
			if rule.Enabled {
				continue
			}
			if _, ok := a.Mapper.Records()[rule.ID]; ok {
				if err := a.Mapper.RemoveMapping(ctx, rule); err == nil {
					removed++
				}
			}
		}
		_ = a.Store.ReconcilePortStatuses()
		return fmt.Sprintf("removed %d stale mappings", removed), nil
	})
	a.Scheduler.Register("discover", func(ctx context.Context, sc model.Schedule) (string, error) {
		gws, err := a.Mapper.Discover(ctx)
		if err != nil {
			return "error", err
		}
		return fmt.Sprintf("found %d gateway(s)", len(gws)), nil
	})
}

// Ensure os import is used for diagnostics.
var _ = os.Getenv

// firstNonZero returns a unless it is zero, in which case b is used. It keeps the
// legacy log.* retention settings working while the newer retention.* section wins.
func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
