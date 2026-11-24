package cases

import (
	"fmt"
	"sort"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

// HistoryEntry adalah satu baris riwayat kasus yang ditampilkan di tab
// "Riwayat". Detail audit mentah tidak pernah diteruskan ke klien.
type HistoryEntry struct {
	Kind       string    `json:"kind"` // STATUS atau ACTION
	At         time.Time `json:"at"`
	Title      string    `json:"title"`
	FromStatus *string   `json:"from_status,omitempty"`
	ToStatus   *string   `json:"to_status,omitempty"`
	Action     string    `json:"action,omitempty"`
	Actor      string    `json:"actor"`
}

// HistoryResponse adalah respons GET /api/cases/{id}/history.
type HistoryResponse struct {
	Items []HistoryEntry `json:"items"`
}

// historyAction adalah aksi audit yang sudah disaring dan siap digabung.
type historyAction struct {
	Action    string
	ActorType string
	ActorName string
	At        time.Time
}

// actionHistoryWhitelist adalah daftar aksi audit yang boleh muncul di riwayat
// kasus. STATUS_CHANGED sengaja tidak ada karena sudah diwakili status_history.
var actionHistoryWhitelist = []string{
	audit.ActionCaseTaken,
	audit.ActionPartyAdded,
	audit.ActionPartyUpdated,
	audit.ActionVerificationRecorded,
	audit.ActionEvidenceUploaded,
	audit.ActionReviewRecorded,
	audit.ActionReviewContinued,
	audit.ActionReviewClosed,
	audit.ActionExamSessionCreated,
	audit.ActionCaseConditionChanged,
	audit.ActionReferralCreated,
	audit.ActionReferralUpdated,
	audit.ActionRecoveryRecorded,
	audit.ActionConclusionSaved,
	audit.ActionConclusionSubmitted,
	audit.ActionDecisionRecorded,
	audit.ActionDeliveryRecorded,
	audit.ActionCaseClosed,
}

// actionTitles adalah label bahasa Indonesia untuk aksi audit. Aksi yang belum
// dipetakan ditampilkan apa adanya agar riwayat tidak pernah kosong.
var actionTitles = map[string]string{
	audit.ActionCaseTaken:            "Kasus diambil",
	audit.ActionPartyAdded:           "Pihak ditambahkan",
	audit.ActionPartyUpdated:         "Data pihak diperbarui",
	audit.ActionVerificationRecorded: "Verifikasi dicatat",
	audit.ActionEvidenceUploaded:     "Bukti diunggah",
	audit.ActionReviewRecorded:       "Penelaahan dicatat",
	audit.ActionReviewContinued:      "Kasus dilanjutkan ke pemeriksaan",
	audit.ActionReviewClosed:         "Kasus tidak dilanjutkan",
	audit.ActionExamSessionCreated:   "Sesi pemeriksaan dibuat",
	audit.ActionCaseConditionChanged: "Kondisi kasus diubah",
	audit.ActionReferralCreated:      "Rujukan dibuat",
	audit.ActionReferralUpdated:      "Rujukan diperbarui",
	audit.ActionRecoveryRecorded:     "Pemantauan pemulihan dicatat",
	audit.ActionConclusionSaved:      "Kesimpulan disimpan",
	audit.ActionConclusionSubmitted:  "Kesimpulan diajukan",
	audit.ActionDecisionRecorded:     "Keputusan dicatat",
	audit.ActionDeliveryRecorded:     "Penyampaian hasil dicatat",
	audit.ActionCaseClosed:           "Kasus ditutup",
}

// statusLabels adalah label status yang ditampilkan ke petugas.
var statusLabels = map[string]string{
	string(workflow.Dilaporkan):      "Dilaporkan",
	string(workflow.PerluDilengkapi): "Perlu dilengkapi",
	string(workflow.Penelaahan):      "Penelaahan",
	string(workflow.Pemeriksaan):     "Pemeriksaan",
	string(workflow.Rekomendasi):     "Rekomendasi",
	string(workflow.Keputusan):       "Keputusan",
	string(workflow.Pemantauan):      "Pemantauan",
	string(workflow.Selesai):         "Selesai",
}

func statusLabel(status string) string {
	if label, ok := statusLabels[status]; ok {
		return label
	}
	return status
}

// mergeHistory menggabungkan riwayat status dan aksi audit dalam urutan waktu.
// Pada waktu yang sama, entri status dipertahankan lebih dulu.
func mergeHistory(statuses []StatusHistoryView, actions []historyAction) []HistoryEntry {
	entries := make([]HistoryEntry, 0, len(statuses)+len(actions))

	for _, h := range statuses {
		to := h.ToStatus
		entry := HistoryEntry{
			Kind:       "STATUS",
			At:         h.ChangedAt,
			FromStatus: h.FromStatus,
			ToStatus:   &to,
			Actor:      statusActor(h),
		}
		if h.FromStatus == nil {
			entry.Title = "Laporan diterima"
		} else {
			entry.Title = fmt.Sprintf("Status: %s → %s",
				statusLabel(*h.FromStatus), statusLabel(h.ToStatus))
		}
		entries = append(entries, entry)
	}

	for _, a := range actions {
		title, ok := actionTitles[a.Action]
		if !ok {
			title = a.Action
		}
		entries = append(entries, HistoryEntry{
			Kind:   "ACTION",
			At:     a.At,
			Title:  title,
			Action: a.Action,
			Actor:  actionActor(a),
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].At.Before(entries[j].At)
	})
	return entries
}

// statusActor menentukan nama aktor pada baris status_history. changed_by NULL
// berarti pelapor (melengkapi laporan) atau sistem (transisi otomatis).
func statusActor(h StatusHistoryView) string {
	if h.ChangedBy != nil && h.ChangedBy.Name != "" {
		return h.ChangedBy.Name
	}
	if h.FromStatus != nil && *h.FromStatus != string(workflow.PerluDilengkapi) {
		return "Sistem"
	}
	return "Pelapor"
}

// actionActor menentukan nama aktor pada audit log.
func actionActor(a historyAction) string {
	if a.ActorName != "" {
		return a.ActorName
	}
	switch a.ActorType {
	case audit.ActorReporter:
		return "Pelapor"
	case audit.ActorSystem:
		return "Sistem"
	default:
		return "Petugas"
	}
}
