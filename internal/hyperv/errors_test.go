package hyperv

import (
	"errors"
	"strings"
	"testing"
)

func TestParseErrorEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		stderr    string
		exitCode  int
		wantErrIs error
		wantMsg   string
	}{
		{
			name:      "ObjectNotFound maps to ErrNotFound",
			stderr:    `{"category":"ObjectNotFound","message":"VM not found","cmdlet":"Get-VM","fullyQualifiedErrorId":"InvalidParameter,Microsoft.HyperV.PowerShell.GetVM"}`,
			exitCode:  1,
			wantErrIs: ErrNotFound,
			wantMsg:   "VM not found",
		},
		{
			name:      "ResourceUnavailable also maps to ErrNotFound",
			stderr:    `{"category":"ResourceUnavailable","message":"resource gone","cmdlet":"Get-VMSwitch"}`,
			exitCode:  1,
			wantErrIs: ErrNotFound,
			wantMsg:   "resource gone",
		},
		{
			name:      "PermissionDenied maps to ErrUnauthorized",
			stderr:    `{"category":"PermissionDenied","message":"access denied","cmdlet":"Set-VM"}`,
			exitCode:  1,
			wantErrIs: ErrUnauthorized,
			wantMsg:   "access denied",
		},
		{
			name:      "InvalidArgument with Vhd FQId maps to ErrInvalidParentPath",
			stderr:    `{"category":"InvalidArgument","fullyQualifiedErrorId":"InvalidParameter,Microsoft.Vhd.PowerShell.Cmdlets.NewVhd","message":"parent missing","cmdlet":"New-VHD"}`,
			exitCode:  1,
			wantErrIs: ErrInvalidParentPath,
			wantMsg:   "parent missing",
		},
		{
			name:      "InvalidArgument with non-Vhd FQId maps to ErrPSExecution",
			stderr:    `{"category":"InvalidArgument","fullyQualifiedErrorId":"InvalidParameter,Microsoft.HyperV.PowerShell.NewVM","message":"bad arg","cmdlet":"New-VM"}`,
			exitCode:  1,
			wantErrIs: ErrPSExecution,
			wantMsg:   "bad arg",
		},
		{
			name:      "unknown category maps to ErrPSExecution",
			stderr:    `{"category":"WriteError","message":"weird","cmdlet":"Foo"}`,
			exitCode:  1,
			wantErrIs: ErrPSExecution,
			wantMsg:   "weird",
		},
		{
			name:      "non-JSON stderr falls back to ErrPSExecution wrapping the bytes",
			stderr:    "Get-VM : a fatal error occurred\n+ At line:1...",
			exitCode:  1,
			wantErrIs: ErrPSExecution,
			wantMsg:   "fatal error",
		},
		{
			name:      "empty stderr still produces ErrPSExecution",
			stderr:    "",
			exitCode:  2,
			wantErrIs: ErrPSExecution,
			wantMsg:   "stderr empty",
		},
		{
			name:      "envelope without cmdlet field still works",
			stderr:    `{"category":"PermissionDenied","message":"denied"}`,
			exitCode:  1,
			wantErrIs: ErrUnauthorized,
			wantMsg:   "denied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := parseErrorEnvelope([]byte(tc.stderr), tc.exitCode)
			if !errors.Is(err, tc.wantErrIs) {
				t.Errorf("err = %v, want errors.Is(_, %v)", err, tc.wantErrIs)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}
