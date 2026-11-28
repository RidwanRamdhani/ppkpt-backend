package decision

import (
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

func fixedNow() time.Time {
	return time.Date(2026, 11, 28, 15, 30, 0, 0, deadline.Jakarta)
}

func validConclusion() ConclusionRequest {
	return ConclusionRequest{
		Result:  "TERBUKTI",
		Summary: "Perbuatan terbukti berdasarkan keterangan saksi dan BAI.",
		Recommendations: []RecommendationRequest{
			{Category: "ADMINISTRATIF", Severity: "SEDANG", Description: "Teguran tertulis"},
		},
	}
}

func TestConclusionValidate(t *testing.T) {
	t.Run("valid terbukti dengan rekomendasi", func(t *testing.T) {
		got, appErr := validConclusion().Validate()
		if appErr != nil {
			t.Fatalf("tidak ingin error: %+v", appErr)
		}
		if got.Result != "TERBUKTI" || len(got.Recommendations) != 1 {
			t.Errorf("hasil = %+v", got)
		}
		if got.Recommendations[0].Category != "ADMINISTRATIF" {
			t.Errorf("kategori = %q", got.Recommendations[0].Category)
		}
	})

	t.Run("valid tidak terbukti tanpa rekomendasi", func(t *testing.T) {
		req := ConclusionRequest{Result: "TIDAK_TERBUKTI", Summary: "Tidak cukup bukti."}
		got, appErr := req.Validate()
		if appErr != nil {
			t.Fatalf("tidak ingin error: %+v", appErr)
		}
		if len(got.Recommendations) != 0 {
			t.Errorf("rekomendasi = %+v", got.Recommendations)
		}
	})

	tests := []struct {
		name   string
		mutate func(*ConclusionRequest)
		field  string
	}{
		{name: "hasil kosong", mutate: func(r *ConclusionRequest) { r.Result = "" }, field: "result"},
		{name: "hasil asing", mutate: func(r *ConclusionRequest) { r.Result = "SEBAGIAN" }, field: "result"},
		{name: "ringkasan kosong", mutate: func(r *ConclusionRequest) { r.Summary = "  " }, field: "summary"},
		{
			name: "terbukti tanpa rekomendasi",
			mutate: func(r *ConclusionRequest) {
				r.Recommendations = nil
			},
			field: "recommendations",
		},
		{
			name: "tidak terbukti dengan rekomendasi",
			mutate: func(r *ConclusionRequest) {
				r.Result = "TIDAK_TERBUKTI"
			},
			field: "recommendations",
		},
		{
			name: "kategori tidak valid",
			mutate: func(r *ConclusionRequest) {
				r.Recommendations[0].Category = "PIDANA"
			},
			field: "recommendations[0].category",
		},
		{
			name: "severity tidak valid",
			mutate: func(r *ConclusionRequest) {
				r.Recommendations[0].Severity = "PARAH"
			},
			field: "recommendations[0].severity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validConclusion()
			tt.mutate(&req)
			_, appErr := req.Validate()
			if appErr == nil || appErr.Status != 422 {
				t.Fatalf("ingin 422, dapat %+v", appErr)
			}
			if appErr.Fields[tt.field] == "" {
				t.Errorf("tidak ada pesan untuk %q: %+v", tt.field, appErr.Fields)
			}
		})
	}

	t.Run("ringkasan melebihi batas", func(t *testing.T) {
		req := validConclusion()
		req.Summary = strings.Repeat("a", maxConclusionSummaryRunes+1)
		_, appErr := req.Validate()
		if appErr == nil || appErr.Fields["summary"] == "" {
			t.Fatalf("ingin error summary, dapat %+v", appErr)
		}
	})
}

func validDecision() DecisionRequest {
	return DecisionRequest{
		DecisionType:   "SANKSI",
		SanctionDetail: "Teguran tertulis dan pembinaan",
		DecidedAt:      "2026-11-28",
	}
}

func TestDecisionValidate(t *testing.T) {
	now := fixedNow()

	t.Run("valid sanksi", func(t *testing.T) {
		got, appErr := validDecision().Validate(now)
		if appErr != nil {
			t.Fatalf("tidak ingin error: %+v", appErr)
		}
		if got.DecisionType != "SANKSI" || got.SanctionDetail == "" {
			t.Errorf("hasil = %+v", got)
		}
		if got.DecidedAt.Format(dateLayout) != "2026-11-28" {
			t.Errorf("tanggal = %v", got.DecidedAt)
		}
	})

	t.Run("tidak terbukti mengabaikan rincian sanksi", func(t *testing.T) {
		got, appErr := DecisionRequest{
			DecisionType:   "TIDAK_TERBUKTI",
			SanctionDetail: "seharusnya diabaikan",
			DecidedAt:      "2026-11-27",
		}.Validate(now)
		if appErr != nil {
			t.Fatalf("tidak ingin error: %+v", appErr)
		}
		if got.SanctionDetail != "" {
			t.Errorf("rincian sanksi = %q, ingin kosong", got.SanctionDetail)
		}
	})

	tests := []struct {
		name   string
		mutate func(*DecisionRequest)
		field  string
	}{
		{name: "jenis kosong", mutate: func(r *DecisionRequest) { r.DecisionType = "" }, field: "decision_type"},
		{name: "jenis asing", mutate: func(r *DecisionRequest) { r.DecisionType = "DAMAI" }, field: "decision_type"},
		{
			name:   "sanksi tanpa rincian",
			mutate: func(r *DecisionRequest) { r.SanctionDetail = "  " },
			field:  "sanction_detail",
		},
		{
			name:   "tanggal masa depan",
			mutate: func(r *DecisionRequest) { r.DecidedAt = "2026-11-29" },
			field:  "decided_at",
		},
		{
			name:   "format tanggal salah",
			mutate: func(r *DecisionRequest) { r.DecidedAt = "28-11-2026" },
			field:  "decided_at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validDecision()
			tt.mutate(&req)
			_, appErr := req.Validate(now)
			if appErr == nil || appErr.Status != 422 {
				t.Fatalf("ingin 422, dapat %+v", appErr)
			}
			if appErr.Fields[tt.field] == "" {
				t.Errorf("tidak ada pesan untuk %q: %+v", tt.field, appErr.Fields)
			}
		})
	}
}

func validDelivery() DeliveryRequest {
	return DeliveryRequest{
		DeliveredTo: "Korban dan pendamping",
		DeliveredAt: "2026-11-28",
		Note:        "Disampaikan langsung",
	}
}

func TestDeliveryValidate(t *testing.T) {
	now := fixedNow()

	t.Run("valid", func(t *testing.T) {
		got, appErr := validDelivery().Validate(now)
		if appErr != nil {
			t.Fatalf("tidak ingin error: %+v", appErr)
		}
		if !got.DeliveredAt.Equal(startOfDay(now)) {
			t.Errorf("tanggal = %v", got.DeliveredAt)
		}
	})

	t.Run("penerima kosong", func(t *testing.T) {
		req := validDelivery()
		req.DeliveredTo = " "
		_, appErr := req.Validate(now)
		if appErr == nil || appErr.Fields["delivered_to"] == "" {
			t.Fatalf("ingin error delivered_to, dapat %+v", appErr)
		}
	})

	t.Run("tanggal masa depan", func(t *testing.T) {
		req := validDelivery()
		req.DeliveredAt = "2026-12-01"
		_, appErr := req.Validate(now)
		if appErr == nil || appErr.Fields["delivered_at"] == "" {
			t.Fatalf("ingin error delivered_at, dapat %+v", appErr)
		}
	})
}

func TestCheckClosePrerequisites(t *testing.T) {
	pemantauan := string(workflow.Pemantauan)
	keputusan := string(workflow.Keputusan)

	tests := []struct {
		name         string
		decisionType string
		status       string
		hasDelivery  bool
		hasRestored  bool
		wantErr      bool
	}{
		{
			name: "sanksi siap ditutup", decisionType: "SANKSI", status: pemantauan,
			hasDelivery: true, wantErr: false,
		},
		{
			name: "sanksi masih keputusan", decisionType: "SANKSI", status: keputusan,
			hasDelivery: true, wantErr: true,
		},
		{
			name: "sanksi tanpa penyampaian", decisionType: "SANKSI", status: pemantauan,
			hasDelivery: false, wantErr: true,
		},
		{
			name: "tidak terbukti dengan pemulihan", decisionType: "TIDAK_TERBUKTI", status: keputusan,
			hasDelivery: true, hasRestored: true, wantErr: false,
		},
		{
			name: "tidak terbukti tanpa pemulihan", decisionType: "TIDAK_TERBUKTI", status: keputusan,
			hasDelivery: true, hasRestored: false, wantErr: true,
		},
		{
			name: "belum ada keputusan", decisionType: "", status: keputusan,
			hasDelivery: true, wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkClosePrerequisites(tt.decisionType, tt.status, tt.hasDelivery, tt.hasRestored)
			if tt.wantErr {
				if err == nil || httpx.FromError(err).Status != 409 {
					t.Fatalf("ingin 409, dapat %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("tidak ingin error: %v", err)
			}
		})
	}
}
