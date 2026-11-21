//go:build !windows

package verify

// shell runs the command with sh -c.
func shell(cmd string) (command, error) {
	return command{path: "sh", args: []string{"-c", cmd}, cleanup: func() {}}, nil
}
