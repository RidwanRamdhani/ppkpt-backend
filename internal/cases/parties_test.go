package cases

import (
	"strings"
	"testing"
)

func strp(v string) *string { return &v }

func TestPartyInputValidate(t *testing.T) {
	valid := PartyInput{
		PartyType:       "saksi",
		Name:            "  Saksi Uji  ",
		Gender:          "PEREMPUAN",
		Category:        "mahasiswa",
		Institution:     "Telkom University",
		RelationToOther: "  teman sekelas  ",
	}
	got, appErr := valid.Validate()
	if appErr != nil {
		t.Fatalf("Validate = %v (fields: %v)", appErr, appErr.Fields)
	}
	if got.PartyType != "SAKSI" || got.Name != "Saksi Uji" ||
		got.Category != "MAHASISWA" || got.RelationToOther != "teman sekelas" {
		t.Fatalf("normalisasi salah: %+v", got)
	}

	tests := []struct {
		name   string
		mutate func(*PartyInput)
		field  string
	}{
		{"tipe korban tidak boleh lewat endpoint ini", func(p *PartyInput) { p.PartyType = "KORBAN" }, "party_type"},
		{"tipe kosong", func(p *PartyInput) { p.PartyType = "" }, "party_type"},
		{"nama kosong", func(p *PartyInput) { p.Name = "   " }, "name"},
		{"gender asing", func(p *PartyInput) { p.Gender = "PRIA" }, "gender"},
		{"kategori asing", func(p *PartyInput) { p.Category = "GURU" }, "category"},
		{"nama terlalu panjang", func(p *PartyInput) { p.Name = strings.Repeat("a", 101) }, "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			tt.mutate(&input)
			_, appErr := input.Validate()
			if appErr == nil || appErr.Fields[tt.field] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.field, appErr)
			}
		})
	}
}

func TestPartyUpdateValidate(t *testing.T) {
	got, appErr := (PartyUpdate{
		Name:     strp("  Nama Baru  "),
		Category: strp("dosen"),
		Faculty:  strp("  FRI  "),
	}).Validate()
	if appErr != nil {
		t.Fatalf("Validate = %v (fields: %v)", appErr, appErr.Fields)
	}
	if got.Name == nil || *got.Name != "Nama Baru" {
		t.Errorf("name = %v", got.Name)
	}
	if got.Category == nil || *got.Category != "DOSEN" {
		t.Errorf("category = %v", got.Category)
	}
	if got.Faculty == nil || *got.Faculty != "FRI" {
		t.Errorf("faculty = %v", got.Faculty)
	}

	if _, appErr := (PartyUpdate{}).Validate(); appErr == nil || appErr.Fields["_"] == "" {
		t.Fatalf("update kosong harus ditolak, dapat %+v", appErr)
	}
	if _, appErr := (PartyUpdate{Name: strp("")}).Validate(); appErr == nil || appErr.Fields["name"] == "" {
		t.Fatalf("nama kosong harus ditolak, dapat %+v", appErr)
	}
	if _, appErr := (PartyUpdate{Gender: strp("PRIA")}).Validate(); appErr == nil || appErr.Fields["gender"] == "" {
		t.Fatalf("gender asing harus ditolak, dapat %+v", appErr)
	}
}

func TestBuildPartyUpdateSQL(t *testing.T) {
	query, args := buildPartyUpdateSQL(11, 22, PartyUpdate{
		Name:    strp("Nama"),
		Faculty: strp(""), // dikirim kosong -> NULL
	})

	for _, want := range []string{"name = $3", "faculty = $4", "WHERE id = $1 AND report_id = $2"} {
		if !strings.Contains(query, want) {
			t.Errorf("query tidak memuat %q: %s", want, query)
		}
	}
	if len(args) != 4 {
		t.Fatalf("args = %#v", args)
	}
	name, ok := args[2].(*string)
	if !ok || name == nil || *name != "Nama" {
		t.Errorf("arg name = %#v", args[2])
	}
	faculty, ok := args[3].(*string)
	if !ok || faculty != nil {
		t.Errorf("faculty kosong harus NULL, dapat %#v", args[3])
	}
}
