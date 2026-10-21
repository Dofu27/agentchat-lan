package main

import (
	"strings"
	"testing"
)

func TestCredentialOutputRedaction(t *testing.T) {
	secret := "SYNTHETIC_VALUE_123"
	for _, text := range []string{
		"Authorization: Bearer " + secret,
		"Authentication: Bearer " + secret,
		"authorization=Basic " + secret,
		"Bearer " + secret,
		"token=" + secret,
		`{"token":"` + secret + `","Authorization":"Bearer ` + secret + `"}`,
		"https://fixture.invalid/path?token=" + secret + "&api_key=" + secret,
		"https://user:" + secret + "@fixture.invalid/",
		"-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----",
	} {
		out := redactOutput(text)
		if strings.Contains(out, secret) {
			t.Fatalf("credential leaked for form %q", strings.Split(text, ":")[0])
		}
		if !strings.Contains(out, "<redacted>") {
			t.Fatal("missing placeholder")
		}
	}
	if got := redactOutput("installed fixture successfully"); got != "installed fixture successfully" {
		t.Fatal(got)
	}
	if got := redactOutput(`{"password":"LEFT_SECRET\"RIGHT_SECRET"}`); strings.Contains(got, "LEFT_SECRET") || strings.Contains(got, "RIGHT_SECRET") {
		t.Fatal("escaped JSON value was only partly redacted")
	}
}

func TestUnterminatedPrivateKeyRedaction(t *testing.T) {
	for _, kind := range []string{"PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY", "OPENSSH PRIVATE KEY"} {
		input := "safe prefix\n-----BEGIN " + kind + "-----\nSYNTHETIC_KEY_PREFIX\nKEY_MATERIAL_LINE\n"
		if got := redactOutput(input); got != "safe prefix\n<redacted>" {
			t.Fatal("unterminated private key was not redacted through EOF")
		}
		if got := redactOutput(input + outputTruncatedMarker); got != "safe prefix\n<redacted>\n"+outputTruncatedMarker {
			t.Fatal("unterminated key consumed truncation metadata or leaked payload")
		}
	}
	complete := "safe prefix\n-----BEGIN PRIVATE KEY-----\nSYNTHETIC_KEY_PREFIX\n-----END PRIVATE KEY-----\nsafe suffix"
	if got := redactOutput(complete); got != "safe prefix\n<redacted>\nsafe suffix" {
		t.Fatal("complete key redaction lost safe following output")
	}
}
