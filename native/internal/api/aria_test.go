package api

import (
	"strings"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAriaInfo_TokenEnforced(t *testing.T) {
	a := &App{ConfigDir: t.TempDir(), DataDir: t.TempDir(), Version: "test", ariaExportToken: "s3cret"}

	rec := httptest.NewRecorder()
	a.handleAriaInfo(rec, httptest.NewRequest("GET", "/api/aria/info", nil))
	if rec.Code != 401 {
		t.Fatalf("no credential: got %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("a 401 should say how to authenticate")
	}

	req := httptest.NewRequest("GET", "/api/aria/info", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	a.handleAriaInfo(rec, req)
	if rec.Code != 200 {
		t.Fatalf("valid token: got %d (%s)", rec.Code, rec.Body.String())
	}
	var info map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["schema"] != "plumb.aria-export/1" || info["token_required"] != true {
		t.Errorf("unexpected info: %v", info)
	}
}

func TestAriaExportFile_RejectsPathTraversal(t *testing.T) {
	a := &App{ConfigDir: t.TempDir(), DataDir: t.TempDir()}
	for _, name := range []string{"..%2f..%2fconfig%2farrays.yml", "..", "a%5Cb.json", "notjson.txt"} {
		req := httptest.NewRequest("GET", "/api/aria/exports/x", nil)
		req.SetPathValue("name", mustUnescape(name))
		rec := httptest.NewRecorder()
		a.handleAriaExportFile(rec, req)
		if rec.Code != 400 {
			t.Errorf("%q: got %d, want 400", name, rec.Code)
		}
	}
}

func mustUnescape(s string) string {
	r := strings.NewReplacer("%2f", "/", "%5C", "\\")
	return r.Replace(s)
}
