package mockhub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func joinToken(t *testing.T, baseURL, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.New(baseURL, "").Join(ctx, protocol.JoinRequest{Name: name, Kind: "test"})
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	return resp.Token
}

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

func TestUploadSanitizesFilename(t *testing.T) {
	ts := newTestHub(t)
	token := joinToken(t, ts.URL, "evil-upload")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "../../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("not a path")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/files", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", resp.StatusCode)
	}
	var ref protocol.FileRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	if ref.Name != "evil.txt" {
		t.Fatalf("sanitized name = %q, want evil.txt", ref.Name)
	}
	if strings.Contains(ref.Name, "..") || strings.Contains(ref.Name, "/") {
		t.Fatalf("unsafe filename survived: %q", ref.Name)
	}
}

func TestDownloadRequiresAuth(t *testing.T) {
	ts := newTestHub(t)
	token := joinToken(t, ts.URL, "download-auth")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "secret.txt")
	_, _ = part.Write([]byte("secret"))
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var ref protocol.FileRef
	_ = json.NewDecoder(resp.Body).Decode(&ref)
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/v1/files/" + ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download status = %d, want 401", resp.StatusCode)
	}
}
