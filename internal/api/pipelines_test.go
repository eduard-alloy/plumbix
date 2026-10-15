package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func postCheck(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/check", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/yaml")
	req.Header.Set("Authorization", "Bearer "+testToken)
	NewHandler("dev", testToken).ServeHTTP(rec, req)
	return rec
}

type checkResponse struct {
	Valid  bool     `json:"valid"`
	Order  []string `json:"order"`
	Errors []struct {
		Path    string `json:"path"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"errors"`
}

func TestCheckPipelineValid(t *testing.T) {
	rec := postCheck(t, "version: 1\nsteps:\n  - {name: b, image: x, run: x, needs: [a]}\n  - {name: a, image: x, run: x}\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp checkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Valid || !reflect.DeepEqual(resp.Order, []string{"a", "b"}) || resp.Errors != nil {
		t.Errorf("response = %+v, want valid with order [a b]", resp)
	}
}

func TestCheckPipelineInvalid(t *testing.T) {
	rec := postCheck(t, "version: 1\nsteps:\n  - {name: a, image: x}\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp checkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Valid || len(resp.Errors) != 1 {
		t.Fatalf("response = %+v, want one error", resp)
	}
	if e := resp.Errors[0]; e.Path != "steps[0].run" || e.Line != 3 || e.Message == "" {
		t.Errorf("error = %+v, want steps[0].run on line 3", e)
	}
}

func TestCheckPipelineTooLarge(t *testing.T) {
	rec := postCheck(t, strings.Repeat("#", MaxPipelineBytes+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"too_large"`) {
		t.Errorf("body = %s, want error code too_large", rec.Body.String())
	}
}
