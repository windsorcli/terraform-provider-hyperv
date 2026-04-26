package hyperv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Resources match against these with `errors.Is(err, X)` to
// decide how to surface to the user (RemoveResource for ErrNotFound,
// AddAttributeError for ErrInvalidParentPath, etc.).
//
// ErrNotFound vs ErrUnavailable is load-bearing: ErrNotFound means the object
// genuinely does not exist (PS ObjectNotFound) and a resource Read should
// RemoveResource so Terraform plans a recreate; ErrUnavailable means the
// object is known but temporarily inaccessible (PS ResourceUnavailable —
// vmms stopped, cluster node fenced, transport blip) and the resource MUST
// surface a transient error rather than dropping the resource from state.
var (
	ErrNotFound          = errors.New("hyperv: resource not found")
	ErrUnavailable       = errors.New("hyperv: resource temporarily unavailable")
	ErrUnauthorized      = errors.New("hyperv: permission denied")
	ErrInvalidParentPath = errors.New("hyperv: invalid parent path")
	ErrPSExecution       = errors.New("hyperv: powershell execution failed")
)

// errorEnvelope mirrors the JSON Write-HypervError emits to stderr.
type errorEnvelope struct {
	Message               string `json:"message"`
	Category              string `json:"category"`
	FullyQualifiedErrorId string `json:"fullyQualifiedErrorId"`
	Cmdlet                string `json:"cmdlet"`
	TargetObject          string `json:"targetObject"`
}

// parseErrorEnvelope decodes the structured envelope on stderr and returns
// the appropriate typed error. Falls back to ErrPSExecution wrapping the
// raw stderr if no envelope is present.
func parseErrorEnvelope(stderr []byte, exitCode int) error {
	trimmed := bytes.TrimSpace(stderr)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: exit %d, stderr empty", ErrPSExecution, exitCode)
	}
	var env errorEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return fmt.Errorf("%w: exit %d: %s", ErrPSExecution, exitCode, string(trimmed))
	}
	base := mapCategory(env)
	if env.Cmdlet != "" {
		return fmt.Errorf("%w: %s (cmdlet=%s)", base, env.Message, env.Cmdlet)
	}
	return fmt.Errorf("%w: %s", base, env.Message)
}

// mapCategory routes a structured-envelope category to the appropriate
// typed sentinel. Spike #3 finding 2 documents the InvalidParameter,Microsoft.Vhd.*
// FQId path that distinguishes a bad differencing parent from generic
// InvalidArgument.
func mapCategory(env errorEnvelope) error {
	switch env.Category {
	case "ObjectNotFound":
		return ErrNotFound
	case "ResourceUnavailable":
		return ErrUnavailable
	case "PermissionDenied":
		return ErrUnauthorized
	case "InvalidArgument":
		if strings.HasPrefix(env.FullyQualifiedErrorId, "InvalidParameter,Microsoft.Vhd.") {
			return ErrInvalidParentPath
		}
		return ErrPSExecution
	default:
		return ErrPSExecution
	}
}
