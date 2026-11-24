package report_test

import (
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

// TestValidateWithManualTanpaEmailDanConsent memastikan input manual tidak
// memaksa email pelapor atau persetujuan digital.
func TestValidateWithManualTanpaEmailDanConsent(t *testing.T) {
	req := validRequest()
	req.Reporter.Email = ""
	req.Consent = false
	req.ConsentTextVersion = ""

	sub, appErr := req.ValidateWith(fixedNow(), report.ManualValidation)
	if appErr != nil {
		t.Fatalf("ValidateWith = %v (fields: %v), ingin valid", appErr, appErr.Fields)
	}
	if sub.Reporter.Email != "" {
		t.Errorf("email = %q, ingin kosong", sub.Reporter.Email)
	}
}

// TestValidateWithManualEmailTetapValid bila pelapor memberi email.
func TestValidateWithManualEmailTetapValid(t *testing.T) {
	req := validRequest()
	req.Consent = false
	req.ConsentTextVersion = ""
	req.Reporter.Email = "manual@example.test"

	sub, appErr := req.ValidateWith(fixedNow(), report.ManualValidation)
	if appErr != nil {
		t.Fatalf("ValidateWith = %v (fields: %v)", appErr, appErr.Fields)
	}
	if sub.Reporter.Email != "manual@example.test" {
		t.Errorf("email = %q", sub.Reporter.Email)
	}
}

// TestValidateWithManualEmailSalahTetapDitolak memastikan opsional bukan
// berarti bebas: email yang diisi harus valid.
func TestValidateWithManualEmailSalahTetapDitolak(t *testing.T) {
	req := validRequest()
	req.Consent = false
	req.ConsentTextVersion = ""
	req.Reporter.Email = "bukan-email"

	_, appErr := req.ValidateWith(fixedNow(), report.ManualValidation)
	requireFields(t, appErr, "reporter.email")
}

// TestValidateWithManualMengabaikanVersiConsent memastikan field consent yang
// tidak relevan tidak menggagalkan input manual.
func TestValidateWithManualMengabaikanVersiConsent(t *testing.T) {
	req := validRequest()
	req.Consent = false
	req.ConsentTextVersion = "0.9"

	if _, appErr := req.ValidateWith(fixedNow(), report.ManualValidation); appErr != nil {
		t.Fatalf("ValidateWith = %v (fields: %v), ingin valid", appErr, appErr.Fields)
	}
}

// TestValidatePortalTetapWajibEmailDanConsent menjaga perilaku form portal.
func TestValidatePortalTetapWajibEmailDanConsent(t *testing.T) {
	req := validRequest()
	req.Reporter.Email = ""
	req.Consent = false

	_, appErr := req.Validate(fixedNow())
	requireFields(t, appErr, "reporter.email", "consent")
}
