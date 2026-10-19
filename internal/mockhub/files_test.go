package mockhub_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentchat-lan/agentchat/internal/protocol"
)

func TestFileUploadSendAndDownload(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()
	src := filepath.Join(dir, "note.txt")
	want := []byte("hello from a file\n")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}

	ref, err := c.UploadFile(ctx, src)
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if ref.ID == "" || ref.Size != int64(len(want)) {
		t.Fatalf("UploadFile() = %#v", ref)
	}
	if ref.SHA256 == "" {
		t.Fatal("UploadFile() returned an empty sha256")
	}

	msg, err := c.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{
		Text:  "file attached",
		Files: []string{ref.ID},
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if len(msg.Files) != 1 || msg.Files[0].ID != ref.ID {
		t.Fatalf("message files = %#v, want one ref to %s", msg.Files, ref.ID)
	}

	dest := filepath.Join(dir, "downloaded.txt")
	info, err := c.DownloadFile(ctx, ref.ID, dest)
	if err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("downloaded content = %q, want %q", got, want)
	}
	if info.SHA256 != ref.SHA256 || info.Size != ref.Size {
		t.Fatalf("DownloadFile() = %#v, want sha %s size %d", info, ref.SHA256, ref.Size)
	}
}

func TestDuplicateUploadIsDeduped(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	src := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(src, []byte("same bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := c.UploadFile(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.UploadFile(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate upload created a new file: %s != %s", first.ID, second.ID)
	}
}

func TestSendUnknownFileRejected(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{
		Text:  "missing file",
		Files: []string{"fil_missing"},
	})
	if err == nil {
		t.Fatal("SendMessage() with an unknown file succeeded, want error")
	}
}
