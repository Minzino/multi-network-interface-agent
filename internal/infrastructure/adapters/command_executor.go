package adapters

import (
	"bytes"
	"context"
	"fmt"
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"
	"os/exec"
	"time"
)

// RealCommandExecutor is a CommandExecutor implementation that executes actual system commands
type RealCommandExecutor struct{}

// NewRealCommandExecutor creates a new RealCommandExecutor
func NewRealCommandExecutor() interfaces.CommandExecutor {
	return &RealCommandExecutor{}
}

// Execute executes a command and returns the result
func (e *RealCommandExecutor) Execute(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, errors.NewSystemError(
			fmt.Sprintf("command execution failed: %s %v", command, args),
			fmt.Errorf("%w, stderr: %s", err, stderr.String()),
		)
	}

	return stdout.Bytes(), nil
}

// ExecuteWithTimeout executes a command with timeout
func (e *RealCommandExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := e.Execute(ctx, command, args...)
	if err != nil {
		// Convert to timeout error when context deadline exceeded
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.NewTimeoutError(
				fmt.Sprintf("command execution timeout: %s %v (timeout: %v)", command, args, timeout),
			)
		}
		return nil, err
	}

	return output, nil
}
