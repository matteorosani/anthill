package domain_test

import (
	"errors"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

var (
	ghRepo      = domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "anthill"}
	adoProject  = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}
	adoCodeRepo = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "web"}
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		name  string
		short string
		repo  domain.Repository
		want  domain.Ref
	}{
		{"github item", "gh-12", ghRepo, domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}},
		{"github pr", "gh-pr-34", ghRepo, domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}},
		{"ado item in project", "ado-12", adoProject, domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 12}},
		{"ado item seen from a code repository belongs to the project", "ado-12", adoCodeRepo, domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 12}},
		{"ado pr", "ado-pr-34", adoCodeRepo, domain.Ref{Repo: adoCodeRepo, Kind: domain.KindPR, Number: 34}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseRef(tt.short, tt.repo)
			if err != nil {
				t.Fatalf("ParseRef(%q) error: %v", tt.short, err)
			}
			if got != tt.want {
				t.Errorf("ParseRef(%q) = %+v, want %+v", tt.short, got, tt.want)
			}
			if s := got.String(); s != tt.short {
				t.Errorf("String() = %q, want %q", s, tt.short)
			}
		})
	}
}

func TestParseRefInvalid(t *testing.T) {
	tests := []struct {
		name  string
		short string
		repo  domain.Repository
	}{
		{"empty", "", ghRepo},
		{"no number", "gh-", ghRepo},
		{"no pr number", "gh-pr-", ghRepo},
		{"zero", "gh-0", ghRepo},
		{"leading zero", "gh-012", ghRepo},
		{"uppercase", "GH-12", ghRepo},
		{"unknown provider", "gl-12", ghRepo},
		{"not a number", "gh-abc", ghRepo},
		{"negative", "gh--12", ghRepo},
		{"too large", "gh-99999999999999999999999", ghRepo},
		{"provider differs from repository", "ado-12", ghRepo},
		{"ado pr without code repository", "ado-pr-34", adoProject},
		{"invalid repository", "gh-12", domain.Repository{Provider: domain.GitHub, Org: "matteorosani"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.ParseRef(tt.short, tt.repo)
			if !errors.Is(err, domain.ErrInvalidRef) {
				t.Errorf("ParseRef(%q) error = %v, want ErrInvalidRef", tt.short, err)
			}
		})
	}
}

func TestRefFull(t *testing.T) {
	tests := []struct {
		ref  domain.Ref
		want string
	}{
		{domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}, "matteorosani/anthill#12"},
		{domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}, "matteorosani/anthill#pr-34"},
		{domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 12}, "contoso/shop#12"},
		{domain.Ref{Repo: adoCodeRepo, Kind: domain.KindPR, Number: 34}, "contoso/shop/web#pr-34"},
	}
	for _, tt := range tests {
		if got := tt.ref.Full(); got != tt.want {
			t.Errorf("Full() = %q, want %q", got, tt.want)
		}
	}
}

func TestRefEqualityComparesRepository(t *testing.T) {
	other := domain.Repository{Provider: domain.GitHub, Org: "someone", Name: "else"}
	a, _ := domain.ParseRef("gh-12", ghRepo)
	b, _ := domain.ParseRef("gh-12", other)
	if a == b {
		t.Errorf("gh-12 in %s and in %s must differ", ghRepo, other)
	}
}

func TestRefDisplay(t *testing.T) {
	otherRepo := domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "other"}
	otherProject := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "hr"}
	tests := []struct {
		name string
		ref  domain.Ref
		from domain.Repository
		want string
	}{
		{"github item", domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 10}, ghRepo, "#10"},
		{"github pr", domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}, ghRepo, "#34"},
		{"github item in another repository", domain.Ref{Repo: otherRepo, Kind: domain.KindItem, Number: 10}, ghRepo, "matteorosani/other#10"},
		{"azure devops item", domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 10}, adoProject, "AB#10"},
		{"azure devops item seen from a code repository", domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 10}, adoCodeRepo, "AB#10"},
		{"azure devops item in another project", domain.Ref{Repo: otherProject, Kind: domain.KindItem, Number: 10}, adoProject, "contoso/hr AB#10"},
		{"azure devops pr", domain.Ref{Repo: adoCodeRepo, Kind: domain.KindPR, Number: 34}, adoCodeRepo, "!34"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.Display(tt.from); got != tt.want {
				t.Errorf("Display() = %q, want %q", got, tt.want)
			}
		})
	}
}
