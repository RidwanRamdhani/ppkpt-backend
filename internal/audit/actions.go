package audit

// Daftar aksi audit. Nilai-nilai ini dipakai lintas modul agar penamaan tetap
// konsisten, mudah dicari, dan mudah diuji. Tambahkan konstanta baru di sini
// ketika ada aksi baru; jangan menulis string literal di service.
const (
	// Autentikasi dan akun.
	ActionLoginSuccess    = "LOGIN_SUCCESS"
	ActionLoginFailed     = "LOGIN_FAILED"
	ActionAccountLocked   = "ACCOUNT_LOCKED"
	ActionLogout          = "LOGOUT"
	ActionPasswordChanged = "PASSWORD_CHANGED"
	ActionUserCreated     = "USER_CREATED"
	ActionUserDeactivated = "USER_DEACTIVATED"

	// Pelaporan dan cek status.
	ActionReportSubmitted  = "REPORT_SUBMITTED"
	ActionReportCompleted  = "REPORT_COMPLETED"
	ActionStatusChecked    = "STATUS_CHECKED"
	ActionStatusChanged    = "STATUS_CHANGED"
	ActionNotificationSent = "NOTIFICATION_SENT"

	// Intake, verifikasi, dan pihak.
	ActionCaseCreated          = "CASE_CREATED"
	ActionCaseTaken            = "CASE_TAKEN"
	ActionCaseViewed           = "CASE_VIEWED"
	ActionVerificationRecorded = "VERIFICATION_RECORDED"
	ActionPartyAdded           = "PARTY_ADDED"
	ActionPartyUpdated         = "PARTY_UPDATED"

	// Penelaahan dan pemeriksaan.
	ActionReviewRecorded      = "REVIEW_RECORDED"
	ActionReviewContinued     = "REVIEW_CONTINUED"
	ActionReviewClosed        = "REVIEW_CLOSED"
	ActionExamSessionCreated  = "EXAM_SESSION_CREATED"
	ActionEvidenceUploaded    = "EVIDENCE_UPLOADED"
	ActionEvidenceDownloaded  = "EVIDENCE_DOWNLOADED"
	ActionDeadlineWarningSent = "DEADLINE_WARNING_SENT"

	// Kondisi, rujukan, dan pemulihan.
	ActionCaseConditionChanged = "CASE_CONDITION_CHANGED"
	ActionReferralCreated      = "REFERRAL_CREATED"
	ActionReferralUpdated      = "REFERRAL_UPDATED"
	ActionRecoveryRecorded     = "RECOVERY_RECORDED"

	// Kesimpulan, keputusan, dan penutupan.
	ActionConclusionSaved     = "CONCLUSION_SAVED"
	ActionConclusionSubmitted = "CONCLUSION_SUBMITTED"
	ActionDecisionRecorded    = "DECISION_RECORDED"
	ActionDeliveryRecorded    = "DELIVERY_RECORDED"
	ActionCaseClosed          = "CASE_CLOSED"

	// Keamanan dan akses.
	ActionForbiddenAccess = "FORBIDDEN_ACCESS"
)
