//go:build windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestBootstrapCreatesExplicitProcessOwner(t *testing.T) {
	f, err := createBootstrapFile(filepath.Join(t.TempDir(), "private-file"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatal("new credential owner differs from initiating process TokenUser")
	}
	if err := validateResumeCredentialSecurity(sd); err != nil {
		t.Fatal("new credential is not eligible for private resume", err)
	}
	t.Logf("native explicit-owner creation passed; process elevated=%v", token.IsElevated())
}

func TestResumeCredentialSecurityDescriptor(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	u := user.User.Sid.String()
	for _, tc := range []struct {
		name, sddl string
		allowed    bool
	}{
		{"private", "O:" + u + "D:P(A;;FA;;;SY)(A;;FA;;;" + u + ")", true},
		{"foreign-owner", "O:BAD:P(A;;FA;;;" + u + ")", false},
		{"unprotected", "O:" + u + "D:(A;;FA;;;" + u + ")", false},
		{"everyone-read", "O:" + u + "D:P(A;;FA;;;" + u + ")(A;;FR;;;WD)", false},
		{"inherited", "O:" + u + "D:P(A;ID;FA;;;" + u + ")", false},
		{"inherit-only-user", "O:" + u + "D:P(A;IO;FA;;;" + u + ")", false},
		{"inherit-only-system", "O:" + u + "D:P(A;;FA;;;" + u + ")(A;IO;FA;;;SY)", false},
		{"null-dacl", "O:" + u + "D:PNO_ACCESS_CONTROL", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateResumeCredentialSecurity(sd); (err == nil) != tc.allowed {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.allowed, err)
			}
		})
	}
}

func TestResumeRefusesWidenedRetainedFileBeforeDatabase(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, extraSID string }{
		{"everyone-read-only", "WD"},
		{"owner-rights-alias-only", "OW"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "retained")
			f, err := createBootstrapFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err = f.WriteString("synthetic-invalid-credential\n"); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			// Baseline uses the actual current-user SID, never OWNER RIGHTS.
			// Exactly one additional trustee isolates each rejection cause.
			sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;" + tc.extraSID + ")")
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			called := false
			ops := defaultBootstrapOps()
			ops.initialize = func(context.Context, string, string) error { called = true; return nil }
			err = bootstrapHub(context.Background(), "unused-db", path, true, ops)
			if err == nil || !strings.Contains(err.Error(), "another account") || called {
				t.Fatalf("extra trustee accepted: %v, initialized=%v", err, called)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("failed resume removed retained file", err)
			}
		})
	}
}
