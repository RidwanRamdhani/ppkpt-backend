// Package cases mengimplementasikan Modul Intake dan Verifikasi Kasus (M04):
// daftar kasus, detail, riwayat, input manual, pengambilan kasus, verifikasi,
// dan pengelolaan pihak. Phase 7 PR 1 memuat sisi baca (daftar, detail,
// riwayat); operasi tulis ditambahkan pada PR berikutnya.
package cases

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultWarnReviewDays = 2
	defaultWarnExamDays   = 7
)

// Actor adalah pemanggil endpoint kasus. Role dipakai untuk memilih proyeksi
// data; UserID dipakai untuk menentukan apakah pemanggil adalah PJ kasus.
type Actor struct {
	UserID int64
	Role   string
}

// Options mengatur ambang peringatan tenggat, tenggat tahap, dan biaya hash
// PIN. Nilai <= 0 diganti default.
type Options struct {
	WarnReviewDays int
	WarnExamDays   int
	ReviewDueDays  int
	ExamDueDays    int
	BcryptCost     int
}

// Service melayani pembacaan dan penulisan kasus. Pool wajib diisi untuk
// operasi database.
type Service struct {
	pool           *pgxpool.Pool
	evidence       *evidence.Service
	audit          *audit.Service
	notifier       *notification.Service
	flow           *workflow.Service
	logger         *slog.Logger
	warnReviewDays int
	warnExamDays   int
	bcryptCost     int
	now            func() time.Time
}

// NewService membuat layanan kasus. evidence/auditor/notifier/logger boleh nil
// agar mudah diuji; di produksi seluruhnya diisi.
func NewService(
	pool *pgxpool.Pool,
	evidenceSvc *evidence.Service,
	auditor *audit.Service,
	notifier *notification.Service,
	logger *slog.Logger,
	opts Options,
) *Service {
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
	cost := opts.BcryptCost
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return &Service{
		pool:     pool,
		evidence: evidenceSvc,
		audit:    auditor,
		notifier: notifier,
		flow: workflow.NewService(auditor, workflow.Options{
			ReviewDueDays: opts.ReviewDueDays,
			ExamDueDays:   opts.ExamDueDays,
		}),
		logger:         logger,
		warnReviewDays: opts.WarnReviewDays,
		warnExamDays:   opts.WarnExamDays,
		bcryptCost:     cost,
		now:            time.Now,
	}
}

// List mengembalikan daftar kasus sesuai filter dan role pemanggil.
//
// Pejabat menerima proyeksi tanpa identitas pihak. Filter needs_action hanya
// berlaku untuk Satgas karena daftar tindakan segera bersifat operasional
// (keputusan D8).
func (s *Service) List(ctx context.Context, actor Actor, f Filter) (ListPage, error) {
	if actor.Role != auth.RoleSatgas && actor.Role != auth.RolePejabat {
		return ListPage{}, httpx.ErrForbidden()
	}
	if f.NeedsAction && actor.Role != auth.RoleSatgas {
		return ListPage{}, httpx.ErrForbidden()
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = defaultPageSize
	}
	if f.PageSize > maxPageSize {
		f.PageSize = maxPageSize
	}

	args := make([]any, 0, 10)
	where := []string{"TRUE"}

	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}

	if f.Query != "" {
		add("r.case_number LIKE $%d", f.Query+"%")
	}
	if f.Status != "" {
		add("r.status = $%d", string(f.Status))
	}
	if f.ViolationType != "" {
		add(`EXISTS (
			SELECT 1 FROM report_violation_types rvt
			JOIN violation_types vt ON vt.id = rvt.violation_type_id
			WHERE rvt.report_id = r.id AND vt.code = $%d)`, f.ViolationType)
	}
	if f.Channel != "" {
		add("r.channel = $%d", f.Channel)
	}
	if f.DateFrom != nil {
		add("r.created_at >= $%d", *f.DateFrom)
	}
	if f.DateTo != nil {
		add("r.created_at < $%d", *f.DateTo)
	}
	switch f.Assigned {
	case "me":
		add("r.assigned_to = $%d", actor.UserID)
	case "none":
		where = append(where, "r.assigned_to IS NULL")
	}

	if f.NeedsAction {
		// Tindakan segera: kasus baru yang belum diambil, laporan yang menunggu
		// pelengkapan, atau kasus milik saya yang tenggatnya mendekati/lewat.
		args = append(args, actor.UserID)
		me := len(args)
		args = append(args, s.warnReviewDays)
		warnReview := len(args)
		args = append(args, s.warnExamDays)
		warnExam := len(args)
		where = append(where, fmt.Sprintf(`(
			(r.status = 'DILAPORKAN' AND r.assigned_to IS NULL)
			OR r.status = 'PERLU_DILENGKAPI'
			OR (
				r.assigned_to = $%d AND (
					(r.status = 'PENELAAHAN' AND r.review_due_at IS NOT NULL
						AND r.review_due_at <= now() + make_interval(days => $%d))
					OR (r.status = 'PEMERIKSAAN' AND r.exam_due_at IS NOT NULL
						AND r.exam_due_at <= now() + make_interval(days => $%d))
				)
			)
		)`, me, warnReview, warnExam))
	}

	whereArgCount := len(args)
	args = append(args, f.PageSize)
	limit := len(args)
	args = append(args, (f.Page-1)*f.PageSize)
	offset := len(args)

	query := listSelectSQL +
		" WHERE " + strings.Join(where, " AND ") +
		" ORDER BY COALESCE(r.case_condition = 'DARURAT', FALSE) DESC, r.created_at DESC, r.id DESC" +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", limit, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return ListPage{}, fmt.Errorf("cases: daftar kasus: %w", err)
	}
	defer rows.Close()

	satgasItems := make([]SatgasListItem, 0, f.PageSize)
	officialItems := make([]OfficialListItem, 0, f.PageSize)
	var total int64

	for rows.Next() {
		var row listRow
		if err := rows.Scan(
			&row.ID,
			&row.CaseNumber,
			&row.Channel,
			&row.Status,
			&row.CaseCondition,
			&row.AssignedID,
			&row.AssignedName,
			&row.ReviewDueAt,
			&row.ExamDueAt,
			&row.CreatedAt,
			&row.ViolationTypes,
			&row.ConclusionResult,
			&row.ConclusionStatus,
			&row.SubmittedAt,
			&row.DecisionType,
			&row.DecidedAt,
			&row.Total,
		); err != nil {
			return ListPage{}, fmt.Errorf("cases: baca daftar kasus: %w", err)
		}
		total = row.Total

		assigned := assignee(row.AssignedID, row.AssignedName)
		types := row.ViolationTypes
		if types == nil {
			types = []string{}
		}

		if actor.Role == auth.RolePejabat {
			conclusion := conclusionSummary(row.ConclusionResult, row.ConclusionStatus, row.SubmittedAt)
			officialItems = append(officialItems, OfficialListItem{
				ID:             row.ID,
				CaseNumber:     row.CaseNumber,
				ViolationTypes: types,
				Channel:        row.Channel,
				Status:         row.Status,
				AssignedTo:     assigned,
				Conclusion:     conclusion,
				Decision:       decisionSummary(row.DecisionType, row.DecidedAt),
				CanReview:      readyToReview(row.Status, conclusion),
				CreatedAt:      row.CreatedAt,
			})
			continue
		}

		satgasItems = append(satgasItems, SatgasListItem{
			ID:             row.ID,
			CaseNumber:     row.CaseNumber,
			ViolationTypes: types,
			Channel:        row.Channel,
			Status:         row.Status,
			CaseCondition:  row.CaseCondition,
			AssignedTo:     assigned,
			Deadline:       s.deadlineView(row.Status, row.ReviewDueAt, row.ExamDueAt),
			CreatedAt:      row.CreatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, fmt.Errorf("cases: iterasi daftar kasus: %w", err)
	}

	// Halaman melewati data terakhir tidak mengembalikan baris, sehingga total
	// dari window function tidak terbaca. Hitung ulang agar pagination tetap
	// menampilkan jumlah yang benar.
	if total == 0 && f.Page > 1 {
		countQuery := "SELECT count(*) FROM reports r WHERE " + strings.Join(where, " AND ")
		if err := s.pool.QueryRow(ctx, countQuery, args[:whereArgCount]...).Scan(&total); err != nil {
			return ListPage{}, fmt.Errorf("cases: hitung daftar kasus: %w", err)
		}
	}

	page := ListPage{Page: f.Page, PageSize: f.PageSize, Total: total}
	if actor.Role == auth.RolePejabat {
		page.Items = officialItems
	} else {
		page.Items = satgasItems
	}
	return page, nil
}

// Detail mengembalikan detail kasus sesuai role. Pejabat hanya boleh membuka
// kasus yang kesimpulannya sudah diajukan; Satgas selalu boleh membuka, dengan
// identitas pelapor disamarkan bila kasus rahasia dan pemanggil bukan PJ.
func (s *Service) Detail(ctx context.Context, actor Actor, reportID int64, ip string) (any, error) {
	if actor.Role != auth.RoleSatgas && actor.Role != auth.RolePejabat {
		return nil, httpx.ErrForbidden()
	}

	access, err := s.loadAccess(ctx, reportID)
	if err != nil {
		return nil, err
	}

	switch actor.Role {
	case auth.RolePejabat:
		if !access.conclusionSubmitted {
			return nil, httpx.ErrForbidden()
		}
		detail, err := s.loadOfficialDetail(ctx, reportID)
		if err != nil {
			return nil, err
		}
		s.logViewed(ctx, actor, reportID, ip)
		return detail, nil
	default: // SATGAS
		detail, err := s.loadSatgasDetail(ctx, reportID, actor.UserID, access)
		if err != nil {
			return nil, err
		}
		s.logViewed(ctx, actor, reportID, ip)
		return detail, nil
	}
}

// History menggabungkan riwayat status dengan aksi kasus yang diizinkan dari
// audit log. Detail audit mentah tidak dikirim agar tidak membocorkan data.
func (s *Service) History(ctx context.Context, actor Actor, reportID int64) (HistoryResponse, error) {
	if actor.Role != auth.RoleSatgas && actor.Role != auth.RolePejabat {
		return HistoryResponse{}, httpx.ErrForbidden()
	}

	access, err := s.loadAccess(ctx, reportID)
	if err != nil {
		return HistoryResponse{}, err
	}
	if actor.Role == auth.RolePejabat && !access.conclusionSubmitted {
		return HistoryResponse{}, httpx.ErrForbidden()
	}

	statuses, err := s.loadStatusHistory(ctx, reportID)
	if err != nil {
		return HistoryResponse{}, err
	}
	actions, err := s.loadActionHistory(ctx, reportID)
	if err != nil {
		return HistoryResponse{}, err
	}
	return HistoryResponse{Items: mergeHistory(statuses, actions)}, nil
}

// logViewed mencatat CASE_VIEWED secara best-effort. Kegagalan audit tidak
// boleh menghalangi petugas membaca kasus.
func (s *Service) logViewed(ctx context.Context, actor Actor, reportID int64, ip string) {
	if s.audit == nil {
		return
	}
	userID := actor.UserID
	err := s.audit.Log(ctx, s.pool, audit.Event{
		ActorType: audit.ActorUser,
		UserID:    &userID,
		Action:    audit.ActionCaseViewed,
		Entity:    "report",
		EntityID:  &reportID,
		IP:        ip,
	})
	if err != nil {
		s.logger.Warn("gagal mencatat audit lihat kasus", "report_id", reportID, "err", err)
	}
}

// deadlineView menghitung sisa tenggat sesuai tahap berjalan. Hanya tahap
// penelaahan dan pemeriksaan yang memiliki tenggat.
func (s *Service) deadlineView(status string, reviewDue, examDue *time.Time) *DeadlineView {
	var (
		due  *time.Time
		warn int
	)
	switch workflow.Status(status) {
	case workflow.Penelaahan:
		due, warn = reviewDue, s.warnReviewDays
	case workflow.Pemeriksaan:
		due, warn = examDue, s.warnExamDays
	default:
		return nil
	}
	if due == nil {
		return nil
	}
	state, daysLeft := deadline.Evaluate(*due, s.now(), warn)
	return &DeadlineView{
		DueAt:    due.In(deadline.Jakarta),
		DaysLeft: daysLeft,
		State:    string(state),
	}
}

// assignee membangun pointer Assignee hanya bila kedua kolom terisi.
func assignee(id *int64, name *string) *Assignee {
	if id == nil || name == nil {
		return nil
	}
	return &Assignee{ID: *id, Name: *name}
}

func decisionSummary(decisionType *string, decidedAt *time.Time) *DecisionSummary {
	if decisionType == nil {
		return nil
	}
	return &DecisionSummary{DecisionType: *decisionType, DecidedAt: decidedAt}
}
