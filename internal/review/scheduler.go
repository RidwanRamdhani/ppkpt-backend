package review

import (
	"context"
	"log/slog"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultWarnReviewDays = 2
	defaultWarnExamDays   = 7
	defaultSchedulerEvery = 24 * time.Hour
)

// SchedulerOptions mengatur ambang peringatan dan ritme pemindaian.
type SchedulerOptions struct {
	WarnReviewDays int
	WarnExamDays   int
	Interval       time.Duration
	Now            func() time.Time
}

// DeadlineWarning adalah satu kasus yang mendekati atau melewati tenggat.
type DeadlineWarning struct {
	ReportID int64
	Stage    workflow.Status
	State    deadline.State
	DaysLeft int
	DueAt    time.Time
}

// Scheduler memindai kasus aktif setiap hari dan mencatat peringatan tenggat.
//
// Status tenggat tetap dihitung ulang saat data dibaca (daftar/detail kasus),
// sehingga kegagalan scheduler tidak membuat angka tenggat menjadi salah
// (SDD M05). Scheduler hanya menambah jejak audit dan log.
type Scheduler struct {
	pool           *pgxpool.Pool
	audit          *audit.Service
	logger         *slog.Logger
	warnReviewDays int
	warnExamDays   int
	interval       time.Duration
	now            func() time.Time
}

// NewScheduler membuat penjadwal tenggat. Nilai opsi yang tidak masuk akal
// diganti default agar scheduler tetap aman dipakai.
func NewScheduler(
	pool *pgxpool.Pool,
	auditor *audit.Service,
	logger *slog.Logger,
	opts SchedulerOptions,
) *Scheduler {
	if auditor == nil {
		auditor = audit.NewService()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if opts.WarnReviewDays <= 0 {
		opts.WarnReviewDays = defaultWarnReviewDays
	}
	if opts.WarnExamDays <= 0 {
		opts.WarnExamDays = defaultWarnExamDays
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultSchedulerEvery
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Scheduler{
		pool:           pool,
		audit:          auditor,
		logger:         logger,
		warnReviewDays: opts.WarnReviewDays,
		warnExamDays:   opts.WarnExamDays,
		interval:       opts.Interval,
		now:            opts.Now,
	}
}

// Run memindai tenggat segera setelah start, lalu setiap interval, sampai
// context dibatalkan.
func (s *Scheduler) Run(ctx context.Context) {
	s.runSafely(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSafely(ctx)
		}
	}
}

func (s *Scheduler) runSafely(ctx context.Context) {
	warnings, err := s.RunOnce(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("gagal memindai tenggat kasus", "err", err)
		}
		return
	}
	if len(warnings) > 0 {
		s.logger.Info("pemindaian tenggat selesai", "warnings", len(warnings))
	}
}

// RunOnce memindai kasus aktif dan mencatat peringatan yang belum dicatat hari
// ini. Peringatan dicatat sekali per kasus per hari agar restart proses tidak
// menggandakan audit.
func (s *Scheduler) RunOnce(ctx context.Context) ([]DeadlineWarning, error) {
	now := s.now()

	rows, err := s.pool.Query(ctx, `
		SELECT id, status, review_due_at, exam_due_at
		FROM reports
		WHERE status IN ('PENELAAHAN', 'PEMERIKSAAN')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	warnings := make([]DeadlineWarning, 0, 8)
	for rows.Next() {
		var (
			reportID  int64
			status    string
			reviewDue *time.Time
			examDue   *time.Time
		)
		if err := rows.Scan(&reportID, &status, &reviewDue, &examDue); err != nil {
			return nil, err
		}
		warning, ok := EvaluateCase(status, reviewDue, examDue, now, s.warnReviewDays, s.warnExamDays)
		if !ok {
			continue
		}
		warning.ReportID = reportID
		warnings = append(warnings, warning)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	warned, err := s.warnedToday(ctx, now)
	if err != nil {
		return warnings, err
	}

	for _, warning := range warnings {
		s.logger.Warn("tenggat kasus",
			"report_id", warning.ReportID,
			"stage", string(warning.Stage),
			"state", string(warning.State),
			"days_left", warning.DaysLeft,
		)
		if _, already := warned[warning.ReportID]; already {
			continue
		}
		reportID := warning.ReportID
		event := audit.Event{
			ActorType: audit.ActorSystem,
			Action:    audit.ActionDeadlineWarningSent,
			Entity:    "report",
			EntityID:  &reportID,
			Detail: map[string]any{
				"stage":     string(warning.Stage),
				"state":     string(warning.State),
				"days_left": warning.DaysLeft,
			},
		}
		if err := s.audit.Log(ctx, s.pool, event); err != nil {
			s.logger.Error("gagal mencatat audit peringatan tenggat",
				"report_id", warning.ReportID, "err", err)
		}
	}
	return warnings, nil
}

// warnedToday mengembalikan himpunan report_id yang sudah mendapat audit
// peringatan tenggat sejak awal hari WIB.
func (s *Scheduler) warnedToday(ctx context.Context, now time.Time) (map[int64]struct{}, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT entity_id
		FROM audit_logs
		WHERE action = 'DEADLINE_WARNING_SENT'
		  AND entity = 'report'
		  AND entity_id IS NOT NULL
		  AND created_at >= $1`,
		startOfDay(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	warned := make(map[int64]struct{}, 8)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		warned[id] = struct{}{}
	}
	return warned, rows.Err()
}

// EvaluateCase menghitung apakah satu kasus perlu peringatan tenggat. Fungsi
// murni ini dipisahkan agar batas hari dan zona waktu mudah diuji.
func EvaluateCase(
	status string,
	reviewDue, examDue *time.Time,
	now time.Time,
	warnReviewDays, warnExamDays int,
) (DeadlineWarning, bool) {
	var (
		due   *time.Time
		stage workflow.Status
		warn  int
	)
	switch workflow.Status(status) {
	case workflow.Penelaahan:
		due, stage, warn = reviewDue, workflow.Penelaahan, warnReviewDays
	case workflow.Pemeriksaan:
		due, stage, warn = examDue, workflow.Pemeriksaan, warnExamDays
	default:
		return DeadlineWarning{}, false
	}
	if due == nil {
		return DeadlineWarning{}, false
	}

	state, daysLeft := deadline.Evaluate(*due, now, warn)
	if state == deadline.Normal {
		return DeadlineWarning{}, false
	}
	return DeadlineWarning{
		Stage:    stage,
		State:    state,
		DaysLeft: daysLeft,
		DueAt:    *due,
	}, true
}
