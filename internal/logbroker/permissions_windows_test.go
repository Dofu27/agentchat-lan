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
		for _, sid := range []string{";;;SY)", ";;;BA)", ";;;" + u.User.Sid.String() + ")"} {
			if !strings.Contains(text, sid) {
				t.Fatalf("missing intended writer ACL: %s", text)
			}
		}
		if strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;AU)") || !strings.Contains(text, "D:P") {
			t.Fatalf("broad/unprotected ACL %s", text)
		}
	}
}
