package auth

// Permission adalah satu aksi yang dapat diotorisasi. Matriks di bawah
// mengikuti Lampiran "Matriks Hak Akses" pada SDD dan diterapkan di server
// (NFR-10). Pemeriksaan tingkat kasus (anggota Satgas penanggung jawab, atau
// pejabat hanya untuk kasus yang sudah diajukan) tetap dilakukan oleh modul
// kasus karena membutuhkan data kasus.
type Permission string

const (
	PermManualReportCreate Permission = "manual_report.create"
	PermCaseList           Permission = "case.list"
	PermCaseView           Permission = "case.view"
	PermCaseTake           Permission = "case.take"
	PermCaseVerify         Permission = "case.verify"
	PermCaseReview         Permission = "case.review"
	PermCaseExamine        Permission = "case.examine"
	PermCaseCondition      Permission = "case.condition"
	PermCaseReferral       Permission = "case.referral"
	PermCaseRecovery       Permission = "case.recovery"
	PermConclusionWrite    Permission = "conclusion.write"
	PermConclusionSubmit   Permission = "conclusion.submit"
	PermDecisionView       Permission = "decision.view"
	PermDecisionRecord     Permission = "decision.record"
	PermDecisionHistory    Permission = "decision.history"
	PermDeliveryRecord     Permission = "delivery.record"
	PermCaseClose          Permission = "case.close"
	PermEvidenceDownload   Permission = "evidence.download"
	PermDashboardView      Permission = "dashboard.view"
)

var permissionMatrix = map[Permission][]string{
	PermManualReportCreate: {RoleSatgas},
	PermCaseList:           {RoleSatgas, RolePejabat},
	PermCaseView:           {RoleSatgas, RolePejabat}, // pejabat: hanya kasus yang diajukan
	PermCaseTake:           {RoleSatgas},
	PermCaseVerify:         {RoleSatgas},
	PermCaseReview:         {RoleSatgas},
	PermCaseExamine:        {RoleSatgas},
	PermCaseCondition:      {RoleSatgas},
	PermCaseReferral:       {RoleSatgas},
	PermCaseRecovery:       {RoleSatgas},
	PermConclusionWrite:    {RoleSatgas},
	PermConclusionSubmit:   {RoleSatgas},
	PermDecisionView:       {RolePejabat},
	PermDecisionRecord:     {RolePejabat},
	PermDecisionHistory:    {RolePejabat},
	PermDeliveryRecord:     {RoleSatgas},
	PermCaseClose:          {RoleSatgas},
	PermEvidenceDownload:   {RoleSatgas, RolePejabat}, // pejabat: lampiran kasus yang diajukan
	PermDashboardView:      {RoleSatgas, RolePejabat},
}

// Allowed melaporkan apakah role boleh melakukan aksi. Aksi yang tidak
// terdaftar selalu ditolak (default deny).
func Allowed(role string, perm Permission) bool {
	for _, r := range permissionMatrix[perm] {
		if r == role {
			return true
		}
	}
	return false
}
