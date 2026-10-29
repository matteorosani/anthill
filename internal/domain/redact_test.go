package domain_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestRedact(t *testing.T) {
	ghToken := "ghp_" + strings.Repeat("a1B2", 9)
	fineGrained := "github_pat_" + strings.Repeat("Ab3_", 20) + "xy"
	privateKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\nabc\n-----END RSA PRIVATE KEY-----"

	tests := []struct {
		name  string
		text  string
		want  string
		local []string
	}{
		{"built-in pattern: github personal access token", "token " + ghToken + " end", "token [REDACTED] end", nil},
		{"github fine-grained token", fineGrained, "[REDACTED]", nil},
		{"cloud key: aws access key id", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", "AWS_ACCESS_KEY_ID=[REDACTED]", nil},
		{"cloud key: google api key", "key=AIzaSyA1234567890abcdefghijklmnopqrstuv", "key=[REDACTED]", nil},
		{"cloud key: azure connection string", "DefaultEndpointsProtocol=https;AccountName=x;AccountKey=abc+/12==;EndpointSuffix=core",
			"DefaultEndpointsProtocol=https;AccountName=x;AccountKey=[REDACTED];EndpointSuffix=core", nil},
		{"private key", "key:\n" + privateKey + "\ndone", "key:\n[REDACTED]\ndone", nil},
		{"bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig", "Authorization: Bearer [REDACTED]", nil},
		{"database password", "Server=db;User Id=sa;Password=hunter2;", "Server=db;User Id=sa;Password=[REDACTED];", nil},
		{"url credentials", "clone https://user:s3cret@example.com/repo.git", "clone https://user:[REDACTED]@example.com/repo.git", nil},
		{"slack token", "xoxb-1234567890-abcdef", "[REDACTED]", nil},
		{"local pattern added", "use ACME-KEY-12345678 now", "use [REDACTED] now", []string{`ACME-KEY-[0-9]{8}`}},
		{"local pattern with secret group", "acme_key: abc123", "acme_key: [REDACTED]", []string{`acme_key: (?P<secret>\w+)`}},
		{"local pattern applies along with built-in ones", "ACME-KEY-12345678 " + ghToken, "[REDACTED] [REDACTED]", []string{`ACME-KEY-[0-9]{8}`}},
		{"plain text is unchanged", "Fix login timeout on Safari; the password field is fine.", "Fix login timeout on Safari; the password field is fine.", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := domain.NewRedactor(tt.local)
			if err != nil {
				t.Fatalf("NewRedactor: %v", err)
			}
			if got := r.Redact(tt.text); got != tt.want {
				t.Errorf("Redact(%q) =\n%q\nwant\n%q", tt.text, got, tt.want)
			}
		})
	}
}

func TestNewRedactorInvalidLocalPattern(t *testing.T) {
	_, err := domain.NewRedactor([]string{"("})
	if err == nil || !strings.Contains(err.Error(), `"("`) {
		t.Errorf("NewRedactor error = %v, want an error naming the pattern", err)
	}
}

func TestTruncate(t *testing.T) {
	t.Run("short text is unchanged", func(t *testing.T) {
		if got := domain.Truncate("hello", 10); got != "hello" {
			t.Errorf("Truncate = %q", got)
		}
	})
	t.Run("exactly at the limit is unchanged", func(t *testing.T) {
		text := strings.Repeat("a", domain.DefaultSummaryLimit)
		if got := domain.Truncate(text, 0); got != text {
			t.Error("text at the default limit was truncated")
		}
	})
	t.Run("truncation with a note", func(t *testing.T) {
		text := strings.Repeat("a", 5000)
		got := domain.Truncate(text, 0)
		if n := utf8.RuneCountInString(got); n > domain.DefaultSummaryLimit {
			t.Errorf("length %d is above the limit", n)
		}
		note := "\n\n… (truncated, 3954 of 5000 characters shown)"
		if !strings.HasSuffix(got, note) {
			t.Errorf("Truncate does not end with %q: ...%q", note, got[len(got)-60:])
		}
		if !strings.HasPrefix(got, strings.Repeat("a", 3954)+"\n") {
			t.Error("Truncate did not keep the start of the text")
		}
	})
	t.Run("counts characters, not bytes", func(t *testing.T) {
		text := strings.Repeat("é🐜", 30) // 60 characters, 180 bytes
		if got := domain.Truncate(text, 60); got != text {
			t.Error("text of 60 characters was truncated at a limit of 60")
		}
		got := domain.Truncate(text, 55)
		if !utf8.ValidString(got) {
			t.Error("truncation cut a character in half")
		}
		if n := utf8.RuneCountInString(got); n > 55 {
			t.Errorf("length %d is above the limit", n)
		}
	})
	t.Run("limit too small for the note", func(t *testing.T) {
		if got := domain.Truncate("abcdefghij", 5); got != "abcde" {
			t.Errorf("Truncate = %q, want %q", got, "abcde")
		}
	})
}

func TestSummaryRedactsBeforeTruncating(t *testing.T) {
	r, err := domain.NewRedactor(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Scenario "Agent output contains a token" (agent-conversation).
	token := "ghp_" + strings.Repeat("x", 36)
	text := strings.Repeat("a ", 25) + token + strings.Repeat(" b", 50)
	got := r.Summary(text, 100)
	if strings.Contains(got, "ghp_") || strings.Contains(got, "xxxx") {
		t.Errorf("Summary leaked part of the token: %q", got)
	}
}
