package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
)

// maxBodyBytes membatasi ukuran body JSON yang diterima.
const maxBodyBytes = 1 << 20 // 1 MiB

// errorResponse adalah bentuk respons error: {"error":{"code":...,"message":...,"fields":...}}.
type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// WriteJSON menulis v sebagai JSON dengan kode status tertentu.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError menulis AppError sebagai respons JSON berformat {"error":{...}}.
func WriteError(w http.ResponseWriter, err error) {
	appErr := FromError(err)
	WriteJSON(w, appErr.Status, errorResponse{Error: errorBody{
		Code:    appErr.Code,
		Message: appErr.Message,
		Fields:  appErr.Fields,
	}})
}

// ReadJSON mendekode body JSON ke dst dengan batas ukuran dan menolak field tak dikenal.
func ReadJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return ErrValidation("Ukuran permintaan terlalu besar.")
		}
		return ErrValidation("Body JSON tidak valid.")
	}
	return nil
}
