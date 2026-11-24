package cases

import (
	"strings"
	"testing"
)

func TestVerificationRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     VerificationRequest
		wantErr string
	}{
		{"lengkap tanpa catatan", VerificationRequest{Result: "lengkap"}, ""},
		{"perlu dengan catatan", VerificationRequest{Result: " PERLU_DILENGKAPI ", NoteForReporter: " Lengkapi bukti. "}, ""},
		{"hasil kosong", VerificationRequest{}, "result"},
		{"hasil asing", VerificationRequest{Result: "DITOLAK"}, "result"},
		{"perlu tanpa catatan", VerificationRequest{Result: "PERLU_DILENGKAPI"}, "note_for_reporter"},
		{"catatan terlalu panjang", VerificationRequest{
			Result: "LENGKAP", NoteForReporter: strings.Repeat("a", maxVerificationNoteRunes+1),
		}, "note_for_reporter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, appErr := tt.req.Validate()
			if tt.wantErr == "" {
				if appErr != nil {
					t.Fatalf("Validate = %v (fields: %v), ingin valid", appErr, appErr.Fields)
				}
				if got.Result != strings.ToUpper(strings.TrimSpace(tt.req.Result)) {
					t.Errorf("result = %q", got.Result)
				}
				if got.NoteForReporter != strings.TrimSpace(tt.req.NoteForReporter) {
					t.Errorf("note = %q", got.NoteForReporter)
				}
				return
			}
			if appErr == nil || appErr.Fields[tt.wantErr] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.wantErr, appErr)
			}
		})
	}
}
