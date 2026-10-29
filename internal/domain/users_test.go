package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestUserSet(t *testing.T) {
	s := domain.NewUserSet("MRosani", "lbianchi")
	for _, tt := range []struct {
		login string
		want  bool
	}{
		{"mrosani", true},
		{"LBIANCHI", true},
		{"outsider", false},
		{"", false},
	} {
		if got := s.Contains(tt.login); got != tt.want {
			t.Errorf("Contains(%q) = %v, want %v", tt.login, got, tt.want)
		}
	}
	var empty domain.UserSet
	if empty.Contains("mrosani") {
		t.Error("the zero UserSet contains a login")
	}
}
