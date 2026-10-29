package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "version command",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "anthill dev\n",
		},
		{
			name:       "version flag",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "anthill dev\n",
		},
		{
			name:       "no arguments",
			args:       nil,
			wantCode:   2,
			wantStderr: "usage: anthill version\n",
		},
		{
			name:       "unknown command",
			args:       []string{"fly"},
			wantCode:   2,
			wantStderr: "anthill: unknown command \"fly\"\nusage: anthill version\n",
		},
		{
			name:       "extra arguments",
			args:       []string{"version", "now"},
			wantCode:   2,
			wantStderr: "anthill: version takes no arguments\nusage: anthill version\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", got, tt.wantStderr)
			}
		})
	}
}

// failingWriter is an io.Writer whose every write fails.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRunReportsWriteFailure(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"version"}, failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
