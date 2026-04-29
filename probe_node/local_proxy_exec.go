package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runProbeLocalCommand(timeout time.Duration, name string, args ...string) (string, error) {
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		return "", fmt.Errorf("empty command")
	}
	d := timeout
	if d <= 0 {
		d = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	cmd := exec.CommandContext(ctx, cleanName, args...)
	hideWindowSysProcAttr(cmd)
	raw, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(raw))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if out != "" {
				return out, fmt.Errorf("run command timeout: %s %s: %w (output: %s)", cleanName, strings.Join(args, " "), ctx.Err(), out)
			}
			return out, fmt.Errorf("run command timeout: %s %s: %w", cleanName, strings.Join(args, " "), ctx.Err())
		}
		if out != "" {
			return out, fmt.Errorf("run command failed: %s %s: %w (output: %s)", cleanName, strings.Join(args, " "), err, out)
		}
		return out, fmt.Errorf("run command failed: %s %s: %w", cleanName, strings.Join(args, " "), err)
	}
	return out, nil
}
