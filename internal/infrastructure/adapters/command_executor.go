package adapters

import (
    "bytes"
    "context"
    "fmt"
    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"
    "os/exec"
    "regexp"
    "strings"
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
    // Basic command/args validation to reduce injection surface (we do not use a shell).
    if !isSafeCommand(command) {
        return nil, errors.NewValidationError(fmt.Sprintf("unsafe command rejected: %s", command), nil)
    }
    for _, a := range args {
        if !isSafeArg(a) {
            return nil, errors.NewValidationError("unsafe argument rejected", nil)
        }
    }

    cmd := exec.CommandContext(ctx, command, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
    if err != nil {
        masked := maskArgs(args)
        return nil, errors.NewSystemError(
            fmt.Sprintf("command execution failed: %s %v", command, masked),
            fmt.Errorf("%w, stderr: %s", err, stderr.String()),
        )
    }

	return stdout.Bytes(), nil
}

// isSafeCommand ensures the binary name has no whitespace or shell metacharacters.
func isSafeCommand(cmd string) bool {
    if cmd == "" { return false }
    if strings.ContainsAny(cmd, " ;|&><`$\n\r\t") { return false }
    return true
}

// isSafeArg performs a conservative check for shell metacharacters in arguments.
func isSafeArg(arg string) bool {
    if strings.ContainsAny(arg, "`$\n\r") { return false }
    return true
}

var secretKeyLike = regexp.MustCompile(`(?i)(pass(word)?|token|secret|key|cred|authorization)`) 

// maskArgs masks likely secret values for logging.
func maskArgs(args []string) []string {
    out := make([]string, len(args))
    for i := range args {
        v := args[i]
        masked := v
        if secretKeyLike.MatchString(v) {
            masked = "***"
        } else if i > 0 && secretKeyLike.MatchString(args[i-1]) {
            masked = "***"
        }
        out[i] = masked
    }
    return out
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
