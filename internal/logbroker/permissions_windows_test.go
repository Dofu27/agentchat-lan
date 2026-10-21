package logbroker

import (
	"golang.org/x/sys/windows"
	"strings"
	"testing"
)

func TestPreparedACLIncludesSystemAndCurrentWriter(t *testing.T) {
	dir := t.TempDir()
	newTestProducer(t, dir)
	if err := PreparePermissions(dir, ""); err != nil {
		t.Fatal(err)
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := permissionPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		text := sd.String()
		for _, sid := range []string{";;;SY)", ";;;BA)"} {
			if !strings.Contains(text, sid) {
				t.Fatalf("missing system/admin ACL: %s", text)
			}
		}
		// SDDL may canonicalize a local account to the LA alias instead of
		// spelling out its S-1-5-21 SID.
		if !strings.Contains(text, ";;;"+u.User.Sid.String()+")") && !strings.Contains(text, ";;;LA)") {
			t.Fatalf("missing intended writer ACL: %s", text)
		}
		if strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;AU)") || !strings.Contains(text, "D:P") {
			t.Fatalf("broad/unprotected ACL %s", text)
		}
	}
}
