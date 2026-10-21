//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBoundedExecutorRedactsTruncatedCredentialOutput(t *testing.T) {
	for _, mode := range []string{"truncated-json", "truncated-key"} {
		t.Run(mode, func(t *testing.T) {
			exe, args, _ := processFixture(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			got, err := runBoundedCommand(ctx, exe, args, 128)
			if err != nil {
				t.Fatal("synthetic output fixture failed", err)
			}
			if strings.Contains(got, "SYNTHETIC") || strings.Contains(got, "KEY_MATERIAL") {
				t.Fatal("executor returned synthetic credential material")
			}
			want := "safe diagnostic\n" + outputTruncatedMarker
			if mode == "truncated-key" {
				want = "safe diagnostic\n<redacted>\n" + outputTruncatedMarker
			}
			if got != want {
				t.Fatal("executor returned unexpected truncation summary")
			}
		})
	}
}

func TestCaptureTruncationCannotExposeCredentialPrefixes(t *testing.T) {
	const safe = "safe diagnostic\n"
	json := safe + `{"token":"SYNTHETIC_JSON_PREFIX_` + strings.Repeat("value", 30) + `"}`
	key := safe + "-----BEGIN PRIVATE KEY-----\nSYNTHETIC_KEY_PREFIX\n" + strings.Repeat("KEY_MATERIAL_LINE\n", 4) + "-----END PRIVATE KEY-----\nsafe suffix"
	for _, tc := range []struct {
		name, input string
		limit       int
		expected    string
	}{
		{"json-middle", json, strings.Index(json, "SYNTHETIC") + 20, safe + outputTruncatedMarker},
		{"json-no-newline", strings.TrimPrefix(json, safe), 30, outputTruncatedMarker},
		{"key-before-end", key, strings.Index(key, "-----END") + 8, safe + "<redacted>\n" + outputTruncatedMarker},
		{"key-middle-body", key, strings.Index(key, "KEY_MATERIAL_LINE") + 5, safe + "<redacted>\n" + outputTruncatedMarker},
	} {
		for _, chunk := range []int{1, 7, 4096} {
			t.Run(fmt.Sprintf("%s/chunk%d", tc.name, chunk), func(t *testing.T) {
				b := &cappedBuffer{limit: tc.limit}
				for offset := 0; offset < len(tc.input); offset += chunk {
					p := []byte(tc.input[offset:min(offset+chunk, len(tc.input))])
					if n, err := b.Write(p); n != len(p) || err != nil {
						t.Fatal("capture violated io.Writer contract")
					}
				}
				if !b.truncated || b.buf.Len() > tc.limit {
					t.Fatal("capture did not enforce its budget")
				}
				got := redactOutput(b.String())
				if strings.Contains(got, "SYNTHETIC") || strings.Contains(got, "KEY_MATERIAL") {
					t.Fatal("truncation exposed synthetic credential material")
				}
				if got != tc.expected {
					t.Fatal("truncation did not retain only safe complete lines and metadata")
				}
			})
		}
	}
}

func TestCaptureKeepsCompleteLinesAndDistinguishesEmptyWrites(t *testing.T) {
	b := &cappedBuffer{limit: 5}
	if n, err := b.Write([]byte("whole")); n != 5 || err != nil {
		t.Fatal("unexpected capture result")
	}
	if n, err := b.Write(nil); n != 0 || err != nil {
		t.Fatal("empty write failed")
	}
	if b.truncated || b.String() != "whole" {
		t.Fatal("exact-budget empty write caused false truncation")
	}
	b = &cappedBuffer{limit: 6}
	_, _ = b.Write([]byte("safe\npartial"))
	if got := redactOutput(b.String()); got != "safe\n"+outputTruncatedMarker {
		t.Fatal("partial final line was retained")
	}
	b = &cappedBuffer{limit: 5}
	_, _ = b.Write([]byte("safe\nextra"))
	if got := redactOutput(b.String()); got != "safe\n"+outputTruncatedMarker {
		t.Fatal("complete final line or truncation marker was lost")
	}
}
