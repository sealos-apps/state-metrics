//nolint:testpackage
package userbalance

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	log "github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	sharedPostgresOnce      sync.Once
	sharedPostgresContainer testcontainers.Container
	sharedPostgresAdminPool *pgxpool.Pool
	sharedPostgresDSNPrefix string
	errSharedPostgres       error
	testDatabaseSequence    atomic.Uint64
)

func TestMain(m *testing.M) {
	code := m.Run()

	if sharedPostgresAdminPool != nil {
		sharedPostgresAdminPool.Close()
	}

	if sharedPostgresContainer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := testcontainers.TerminateContainer(
			sharedPostgresContainer,
			testcontainers.StopContext(ctx),
			testcontainers.StopTimeout(time.Second),
		)

		cancel()

		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "terminate shared postgres container: %v\n", err)
			code = 1
		}
	}

	os.Exit(code)
}

func TestPollSkipsCollectionWhenNoUsersAndPositiveDiscoveryDisabled(t *testing.T) {
	c := newTestCollector(t, NewDefaultConfig(), nil, nil)

	if err := c.Poll(context.Background()); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if len(metrics) != 0 {
		t.Fatalf("metrics = %d, want 0", len(metrics))
	}
}

func TestDefaultConfigDisablesLocalOwnerDatabase(t *testing.T) {
	cfg := NewDefaultConfig()
	if cfg.DatabaseConfig.LocalDSN != "" {
		t.Fatalf("local DSN = %q, want empty", cfg.DatabaseConfig.LocalDSN)
	}
}

func TestPollConfiguredUsersEmitsBalanceAndQuota(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "gold",
			Quota:  42.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "gold",
			"source": "configured",
		},
		10.35,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "quota",
			"level":  "gold",
			"source": "configured",
		},
		42.5,
	)
}

func TestPollPositiveBalanceUsersIncludesConfiguredUsersWithNonPositiveBalance(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "bob-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "silver",
			Quota:  7.25,
		},
	}
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 4; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "bob-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "silver",
		},
		0,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "bob-id",
			"owner":  "configured-owner",
			"type":   "quota",
			"level":  "silver",
		},
		7.25,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "region-a",
			"uuid":   "00000000-0000-0000-0000-000000000001",
			"uid":    "alice-id",
			"owner":  "alice-owner",
			"type":   "balance",
			"level":  "default",
			"source": "discovered",
		},
		10.35,
	)
}

func TestPollPositiveBalanceUsersUsesConfiguredLabelsForDiscoveredUser(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "gold",
			Quota:  42.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 3; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "gold",
			"source": "configured",
		},
		10.35,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "quota",
			"level":  "gold",
			"source": "configured",
		},
		42.5,
	)
	assertUserBalanceMetricAbsent(
		t,
		metrics,
		map[string]string{"owner": "alice-owner"},
	)
}

func TestPollOwnerQueryFailurePreservesConfiguredUsers(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	if _, err := databases.local.Exec(ctx, `DROP TABLE "UserCr"`); err != nil {
		t.Fatalf("drop UserCr table: %v", err)
	}

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "gold",
			Quota:  42.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "gold",
			"source": "configured",
		},
		10.35,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "quota",
			"level":  "gold",
			"source": "configured",
		},
		42.5,
	)
}

func TestPollPositiveBalanceDiscoveryFailurePreservesConfiguredUsers(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	if _, err := databases.global.Exec(
		ctx,
		`ALTER TABLE "Account" DROP COLUMN create_region_id`,
	); err != nil {
		t.Fatalf("drop Account.create_region_id: %v", err)
	}

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "gold",
			Quota:  42.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"source": "configured",
		},
		10.35,
	)
}

func TestPollConfiguredUsersContinuesAfterMissingUser(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "missing-id",
			Owner:  "missing-owner",
			Type:   "standard",
			Level:  "basic",
			Quota:  1,
		},
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "gold",
			Quota:  42.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceMetricAbsent(
		t,
		metrics,
		map[string]string{"uid": "missing-id"},
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "gold",
		},
		10.35,
	)
}

func TestPollConfiguredUsersPreservesDistinctLevels(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "gold",
			Quota:  42.5,
		},
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "alice-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "platinum",
			Quota:  50,
		},
	}
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 4; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	for _, level := range []string{"gold", "platinum"} {
		assertUserBalanceGauge(
			t,
			metrics,
			map[string]string{
				"region": "configured-region",
				"uuid":   "configured-uuid",
				"uid":    "alice-id",
				"owner":  "configured-owner",
				"type":   "standard",
				"level":  level,
			},
			10.35,
		)
	}
}

func TestPollConfiguredUsersDefaultsEmptyRegionAndLevel(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.UserConfig = []UserConfig{
		{
			UUID:  "configured-uuid",
			UID:   "alice-id",
			Owner: "configured-owner",
			Type:  "standard",
			Quota: 42.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "default",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "default",
		},
		10.35,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "default",
			"uuid":   "configured-uuid",
			"uid":    "alice-id",
			"owner":  "configured-owner",
			"type":   "quota",
			"level":  "default",
		},
		42.5,
	)
}

func TestPollConfiguredUserWithoutAccountEmitsZeroBalance(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.UserConfig = []UserConfig{
		{
			Region: "configured-region",
			UUID:   "configured-uuid",
			UID:    "noaccount-id",
			Owner:  "configured-owner",
			Type:   "standard",
			Level:  "basic",
			Quota:  3.5,
		},
	}
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "noaccount-id",
			"owner":  "configured-owner",
			"type":   "standard",
			"level":  "basic",
		},
		0,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "configured-region",
			"uuid":   "configured-uuid",
			"uid":    "noaccount-id",
			"owner":  "configured-owner",
			"type":   "quota",
			"level":  "basic",
		},
		3.5,
	)
}

func TestPollPositiveBalanceUsersAutoDiscoversOwners(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "region-a",
			"uuid":   "00000000-0000-0000-0000-000000000001",
			"uid":    "alice-id",
			"owner":  "alice-owner",
			"type":   "balance",
			"level":  "default",
			"source": "discovered",
		},
		10.35,
	)
	assertUserBalanceGauge(
		t,
		metrics,
		map[string]string{
			"region": "default",
			"uuid":   "00000000-0000-0000-0000-000000000006",
			"uid":    "empty-region-id",
			"owner":  "empty-region-owner",
			"type":   "balance",
			"level":  "default",
			"source": "discovered",
		},
		8,
	)
	assertUserBalanceMetricAbsent(
		t,
		metrics,
		map[string]string{"owner": "bob-owner"},
	)
	assertUserBalanceMetricAbsent(
		t,
		metrics,
		map[string]string{"owner": "carol-owner"},
	)
	assertUserBalanceMetricAbsent(
		t,
		metrics,
		map[string]string{"uid": "orphan-id"},
	)
}

func TestPollPositiveBalanceUsersEmitsEachRegionalOwner(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	if _, err := databases.local.Exec(
		ctx,
		`INSERT INTO "UserCr" ("userUid", "crName") VALUES
		 ('00000000-0000-0000-0000-000000000001', 'alice-second-owner')`,
	); err != nil {
		t.Fatalf("insert second regional owner: %v", err)
	}

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 3; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	for _, owner := range []string{"alice-owner", "alice-second-owner"} {
		assertUserBalanceGauge(
			t,
			metrics,
			map[string]string{
				"uid":    "alice-id",
				"owner":  owner,
				"source": "discovered",
			},
			10.35,
		)
	}
}

func TestPollPositiveBalanceUsersWithoutLocalDatabaseLeavesOwnersEmpty(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.PositiveBalanceUsers = true
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 3; got != want {
		t.Fatalf("metrics = %d, want %d", got, want)
	}

	for _, uid := range []string{"alice-id", "orphan-id", "empty-region-id"} {
		assertUserBalanceGauge(
			t,
			metrics,
			map[string]string{
				"uid":    uid,
				"owner":  "",
				"source": "discovered",
			},
			map[string]float64{
				"alice-id":        10.35,
				"orphan-id":       9,
				"empty-region-id": 8,
			}[uid],
		)
	}
}

func TestPollPositiveBalanceUsersSkipsDiscoveryWhenLocalDatabaseUnavailable(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	c := newTestCollector(t, cfg, databases.global, nil)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if len(metrics) != 0 {
		t.Fatalf("metrics = %d, want 0", len(metrics))
	}
}

func TestPollPositiveBalanceUsersClearsStaleMetrics(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databases := startPostgresContainer(t, ctx)
	seedUserBalancePostgres(t, ctx, databases)

	cfg := NewDefaultConfig()
	cfg.DatabaseConfig.LocalDSN = "configured"
	cfg.PositiveBalanceUsers = true
	c := newTestCollector(t, cfg, databases.global, databases.local)

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance: %v", err)
	}

	metrics := collectUserBalanceMetrics(t, c)
	if got, want := len(metrics), 2; got != want {
		t.Fatalf("first poll metrics = %d, want %d", got, want)
	}

	if _, err := databases.global.Exec(
		ctx,
		`UPDATE "Account" SET balance = 0, deduction_balance = 0`,
	); err != nil {
		t.Fatalf("clear positive balance: %v", err)
	}

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll userbalance after balance update: %v", err)
	}

	metrics = collectUserBalanceMetrics(t, c)
	if len(metrics) != 0 {
		t.Fatalf("metrics = %d, want 0", len(metrics))
	}
}

func newTestCollector(
	t *testing.T,
	cfg *Config,
	globalPool, localPool *pgxpool.Pool,
) *Collector {
	t.Helper()

	logger := log.New()
	logger.SetOutput(io.Discard)

	c := &Collector{
		BaseCollector: base.NewBaseCollector("userbalance", log.NewEntry(logger)),
		config:        cfg,
		logger:        log.NewEntry(logger),
		pgClient:      globalPool,
		localPgClient: localPool,
		balances:      make(map[string]balanceSample),
	}
	c.initMetrics("test")

	return c
}

type testPostgresDatabases struct {
	global *pgxpool.Pool
	local  *pgxpool.Pool
}

func startPostgresContainer(t *testing.T, ctx context.Context) *testPostgresDatabases {
	t.Helper()

	sharedPostgresOnce.Do(func() {
		errSharedPostgres = startSharedPostgres(ctx)
	})

	if errSharedPostgres != nil {
		t.Fatalf("start shared postgres container: %v", errSharedPostgres)
	}

	sequence := testDatabaseSequence.Add(1)
	globalDatabase := fmt.Sprintf("globaldb_%d", sequence)

	localDatabase := fmt.Sprintf("localdb_%d", sequence)
	for _, databaseName := range []string{globalDatabase, localDatabase} {
		if _, err := sharedPostgresAdminPool.Exec(
			ctx,
			"CREATE DATABASE "+databaseName,
		); err != nil {
			t.Fatalf("create postgres database %s: %v", databaseName, err)
		}
	}

	globalPool := connectTestPostgres(
		t,
		ctx,
		sharedPostgresDSNPrefix+"/"+globalDatabase+"?sslmode=disable",
	)
	localPool := connectTestPostgres(
		t,
		ctx,
		sharedPostgresDSNPrefix+"/"+localDatabase+"?sslmode=disable",
	)

	return &testPostgresDatabases{
		global: globalPool,
		local:  localPool,
	}
}

func startSharedPostgres(ctx context.Context) error {
	waitStrategy := wait.ForListeningPort("5432/tcp").
		WithStartupTimeout(90 * time.Second)

	ctr, err := testcontainers.Run(
		ctx,
		"postgres:16-alpine",
		testcontainers.WithEnv(map[string]string{
			"POSTGRES_DB":       "postgres",
			"POSTGRES_PASSWORD": "postgres",
			"POSTGRES_USER":     "postgres",
		}),
		testcontainers.WithExposedPorts("5432/tcp"),
		testcontainers.WithWaitStrategy(waitStrategy),
	)
	if err != nil {
		return fmt.Errorf("run postgres container: %w", err)
	}

	sharedPostgresContainer = ctr

	host, err := ctr.Host(ctx)
	if err != nil {
		return fmt.Errorf("get postgres host: %w", err)
	}

	port, err := ctr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return fmt.Errorf("get postgres port: %w", err)
	}

	sharedPostgresDSNPrefix = "postgres://postgres:postgres@" +
		net.JoinHostPort(host, port.Port())

	sharedPostgresAdminPool, err = pgxpool.New(
		ctx,
		sharedPostgresDSNPrefix+"/postgres?sslmode=disable",
	)
	if err != nil {
		return fmt.Errorf("connect postgres admin database: %w", err)
	}

	if err := sharedPostgresAdminPool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres admin database: %w", err)
	}

	return nil
}

func connectTestPostgres(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}

	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	return pool
}

func seedUserBalancePostgres(
	t *testing.T,
	ctx context.Context,
	databases *testPostgresDatabases,
) {
	t.Helper()

	globalStatements := []string{
		`CREATE TABLE "User" (
			uid uuid PRIMARY KEY,
			id text NOT NULL UNIQUE,
			name text NOT NULL
		)`,
		`CREATE TABLE "Account" (
			"userUid" uuid PRIMARY KEY,
			create_region_id text NOT NULL DEFAULT '',
			balance bigint NOT NULL DEFAULT 0,
			deduction_balance bigint NOT NULL DEFAULT 0,
			"encryptBalance" text NOT NULL DEFAULT ''
		)`,
		`INSERT INTO "User" (uid, id, name) VALUES
			('00000000-0000-0000-0000-000000000001', 'alice-id', 'Alice'),
			('00000000-0000-0000-0000-000000000002', 'bob-id', 'Bob'),
			('00000000-0000-0000-0000-000000000003', 'carol-id', 'Carol'),
			('00000000-0000-0000-0000-000000000004', 'orphan-id', 'Orphan'),
			('00000000-0000-0000-0000-000000000005', 'noaccount-id', 'No Account'),
			('00000000-0000-0000-0000-000000000006', 'empty-region-id', 'Empty Region')`,
		`INSERT INTO "Account" ("userUid", create_region_id, balance, deduction_balance) VALUES
			('00000000-0000-0000-0000-000000000001', 'region-a', 12345678, 2000000),
			('00000000-0000-0000-0000-000000000002', 'region-b', 5000000, 5000000),
			('00000000-0000-0000-0000-000000000003', 'region-c', 3000000, 4000000),
			('00000000-0000-0000-0000-000000000004', 'region-d', 9000000, 0),
			('00000000-0000-0000-0000-000000000006', '', 8000000, 0)`,
	}
	localStatements := []string{
		`CREATE TABLE "UserCr" (
			"userUid" uuid NOT NULL,
			"crName" text NOT NULL UNIQUE
		)`,
		`INSERT INTO "UserCr" ("userUid", "crName") VALUES
			('00000000-0000-0000-0000-000000000001', 'alice-owner'),
			('00000000-0000-0000-0000-000000000002', 'bob-owner'),
			('00000000-0000-0000-0000-000000000003', 'carol-owner'),
			('00000000-0000-0000-0000-000000000006', 'empty-region-owner')`,
	}

	execTestStatements(t, ctx, databases.global, globalStatements)
	execTestStatements(t, ctx, databases.local, localStatements)
}

func execTestStatements(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	statements []string,
) {
	t.Helper()

	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("seed postgres statement %q: %v", statement, err)
		}
	}
}

type collectedUserBalanceMetric struct {
	labels map[string]string
	value  float64
}

func collectUserBalanceMetrics(t *testing.T, c *Collector) []collectedUserBalanceMetric {
	t.Helper()

	ch := make(chan prometheus.Metric, 16)
	c.collect(ch)
	close(ch)

	var metrics []collectedUserBalanceMetric
	for metric := range ch {
		var dtoMetric dto.Metric
		if err := metric.Write(&dtoMetric); err != nil {
			t.Fatalf("write metric: %v", err)
		}

		labels := make(map[string]string, len(dtoMetric.GetLabel()))
		for _, label := range dtoMetric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}

		metrics = append(metrics, collectedUserBalanceMetric{
			labels: labels,
			value:  dtoMetric.GetGauge().GetValue(),
		})
	}

	return metrics
}

func assertUserBalanceGauge(
	t *testing.T,
	metrics []collectedUserBalanceMetric,
	labels map[string]string,
	want float64,
) {
	t.Helper()

	for _, metric := range metrics {
		if !userBalanceLabelsMatch(metric.labels, labels) {
			continue
		}

		if metric.value != want {
			t.Fatalf("metric %v = %v, want %v", labels, metric.value, want)
		}

		return
	}

	t.Fatalf("metric with labels %v not found in %v", labels, metrics)
}

func assertUserBalanceMetricAbsent(
	t *testing.T,
	metrics []collectedUserBalanceMetric,
	labels map[string]string,
) {
	t.Helper()

	for _, metric := range metrics {
		if userBalanceLabelsContained(metric.labels, labels) {
			t.Fatalf("unexpected metric with labels %v", labels)
		}
	}
}

func userBalanceLabelsMatch(got, want map[string]string) bool {
	return userBalanceLabelsContained(got, want)
}

func userBalanceLabelsContained(got, want map[string]string) bool {
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}

	return true
}
