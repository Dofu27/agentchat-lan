package cli

import (
	"fmt"
	"github.com/Dofu27/agentchat-lan/internal/logbroker"
	"golang.org/x/sys/windows/svc"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStructuredServiceChild(t *testing.T) {
	if os.Getenv("AGENTCHAT_TEST_SERVICE_CHILD") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "SYNTHETIC_SECRET_CHILD_STDOUT")
	fmt.Fprintln(os.Stderr, "SYNTHETIC_SECRET_CHILD_STDERR")
	os.Exit(3)
}
func TestServiceSupervisorMetadataNotChildOutput(t *testing.T) {
	t.Setenv("AGENTCHAT_TEST_SERVICE_CHILD", "1")
	dir := t.TempDir()
	s := &childService{exe: os.Args[0], args: []string{"-test.run=^TestStructuredServiceChild$"}, logging: logbroker.Options{Dir: dir, Component: "service", MaxBytes: 64 << 10, Files: 3}}
	status := make(chan svc.Status, 10)
	done := make(chan uint32, 1)
	go func() { _, code := s.Execute(nil, make(chan svc.ChangeRequest), status); done <- code }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatal(code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not finish")
	}
	b, e := (logbroker.Reader{Dir: dir}).Read(logbroker.Query{Component: "service", Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range b.Entries {
		found = found || entry.Record.Event == "child_failed"
		if strings.Contains(fmt.Sprint(entry), "SYNTHETIC_SECRET") {
			t.Fatal("child output collected")
		}
	}
	if !found {
		t.Fatal("missing supervisor failure event")
	}
}
