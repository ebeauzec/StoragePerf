package ariaexport

import (
	"net/http/httptest"
	"testing"
)

func TestDownsample(t *testing.T) {
	var pts [][2]float64
	for i := 0; i < 300; i++ {
		pts = append(pts, [2]float64{float64(i), float64(i) * 2})
	}
	got := Downsample(pts, 48)
	if len(got) != 48 {
		t.Fatalf("want 48 points, got %d", len(got))
	}
	if got[len(got)-1] != pts[len(pts)-1] {
		t.Errorf("the most recent point must survive: got %v want %v", got[len(got)-1], pts[len(pts)-1])
	}
	for i := 1; i < len(got); i++ {
		if got[i][0] <= got[i-1][0] {
			t.Fatalf("points must stay in time order: %v then %v", got[i-1], got[i])
		}
	}
	short := pts[:10]
	if len(Downsample(short, 48)) != 10 {
		t.Error("a series shorter than the cap must be returned untouched")
	}
}

func TestAuthorized(t *testing.T) {
	req := func(setup func(r *httptest.ResponseRecorder)) {}
	_ = req
	cases := []struct {
		name   string
		token  string
		header map[string]string
		query  string
		want   bool
	}{
		{"no token configured allows everyone", "", nil, "", true},
		{"missing credential", "s3cret", nil, "", false},
		{"bearer", "s3cret", map[string]string{"Authorization": "Bearer s3cret"}, "", true},
		{"x-plumb-token", "s3cret", map[string]string{"X-Plumb-Token": "s3cret"}, "", true},
		{"query", "s3cret", nil, "?token=s3cret", true},
		{"wrong bearer", "s3cret", map[string]string{"Authorization": "Bearer nope"}, "", false},
		{"prefix of the token is not enough", "s3cret", map[string]string{"Authorization": "Bearer s3cre"}, "", false},
		{"basic scheme is not accepted", "s3cret", map[string]string{"Authorization": "Basic s3cret"}, "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/aria/export"+c.query, nil)
		for k, v := range c.header {
			r.Header.Set(k, v)
		}
		if got := Authorized(r, c.token); got != c.want {
			t.Errorf("%s: Authorized = %v, want %v", c.name, got, c.want)
		}
	}
}
