// Maintainer-only diagnostic: exercises gokrb5's password-mode AS-REQ
// directly, with verbose error reporting, to disambiguate AD-vs-gokrb5
// preauth issues from masterzen/winrm wrapping noise. Reads HVLAB_*
// and the local krb5.conf; prints the full error chain on failure and
// the TGT principal on success.
//
//	set -a; source .env.local; set +a
//	go run ./hack/krb5-probe
package main

import (
	"fmt"
	"os"

	"github.com/jcmturner/gokrb5/v8/client"
	"github.com/jcmturner/gokrb5/v8/config"
)

func main() {
	confPath := os.Getenv("KRB5_CONFIG")
	if confPath == "" {
		confPath = os.Getenv("HOME") + "/.config/krb5.conf"
	}
	cfg, err := config.Load(confPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config.Load:", err)
		os.Exit(1)
	}

	username := os.Getenv("HVLAB_ADMIN_USERNAME")
	if username == "" {
		username = "Administrator"
	}
	realm := os.Getenv("HVLAB_KRB5_REALM")
	if realm == "" {
		realm = "HV.LAB"
	}
	password := os.Getenv("HVLAB_ADMIN_PASSWORD")
	if password == "" {
		fmt.Fprintln(os.Stderr, "HVLAB_ADMIN_PASSWORD not set")
		os.Exit(1)
	}

	cl := client.NewWithPassword(username, realm, password, cfg,
		client.DisablePAFXFAST(true), client.AssumePreAuthentication(true))
	if err := cl.Login(); err != nil {
		// gokrb5 wraps KRBError in krberror.Krberror, which has its own
		// Error() but doesn't implement Unwrap() — so errors.As can't
		// reach the inner KRBError to read ErrorCode/EText/EData. The
		// formatted error message already contains the numeric code,
		// symbolic name, and e-text in human-readable form, which is
		// what a maintainer eyeballs anyway.
		fmt.Fprintln(os.Stderr, "Login:", err)
		os.Exit(1)
	}
	fmt.Println("Login OK; client principal:", cl.Credentials.CName().PrincipalNameString())
}
