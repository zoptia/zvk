package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanfinn/tls-client/profiles"
)

func TestChromeHeadersFollowProfile(t *testing.T) {
	for _, tt := range []struct {
		name  string
		major int
	}{
		{"", 150}, {"CHROME", 150}, {"chrome-latest", 150}, {"latest", 150}, {"default", 150},
		{"chrome_133", 133}, {"chrome_150", 150}, {"chrome_152", 152}, {"CHROME_152_psk", 152},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := resolveProfile(tt.name)
			if !ok {
				t.Fatal("profile not found")
			}
			id := p.GetClientHelloId()
			if id.Client != "Chrome" || !strings.HasPrefix(id.Version, fmt.Sprint(tt.major)) {
				t.Fatalf("wrong TLS identity: %s", p.GetClientHelloStr())
			}
			if _, err := p.GetClientHelloSpec(); err != nil {
				t.Fatalf("invalid ClientHello spec: %v", err)
			}
			h := buildFetchHeaders(fetchOptions{profile: tt.name})
			ua := h["user-agent"][0]
			if !strings.Contains(ua, fmt.Sprintf("Chrome/%d.0.0.0", tt.major)) {
				t.Fatalf("TLS/header mismatch: %s / %s", p.GetClientHelloStr(), ua)
			}
			hints := h["sec-ch-ua"][0]
			for _, brand := range []string{"Chromium", "Google Chrome"} {
				if !strings.Contains(hints, fmt.Sprintf(`"%s";v="%d"`, brand, tt.major)) {
					t.Fatalf("Client Hints mismatch: %s", hints)
				}
			}
			if h["sec-ch-ua-platform"][0] != chromePlatform() {
				t.Fatal("wrong platform hint")
			}
		})
	}
}

func TestAllFetchProfileNamesResolve(t *testing.T) {
	for name, want := range profiles.MappedTLSClients {
		for _, input := range []string{name, strings.ToLower(name), strings.ToUpper(name)} {
			got, ok := resolveProfile(input)
			if !ok || got.GetClientHelloStr() != want.GetClientHelloStr() {
				t.Errorf("cannot select listed profile %q", input)
			}
		}
	}
	if _, ok := resolveProfile("chrome_nonexistent"); ok {
		t.Fatal("accepted unknown profile")
	}
}

func TestChromeClientHintBrands(t *testing.T) {
	// Fixed expected values catch changes to the GREASE version/brand permutation.
	for major, want := range map[int]string{
		133: `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`,
		150: `"Not;A=Brand";v="8", "Chromium";v="150", "Google Chrome";v="150"`,
		152: `"Chromium";v="152", "Not?A_Brand";v="24", "Google Chrome";v="152"`,
	} {
		if got := chromeSecChUa(major); got != want {
			t.Errorf("Chrome %d: got %s, want %s", major, got, want)
		}
	}
}

func TestFetchHeaderOverrides(t *testing.T) {
	h := buildFetchHeaders(fetchOptions{profile: "chrome_152_PSK", userAgent: "custom-agent", headers: []string{"Sec-CH-UA: custom-hints", "Sec-CH-UA-Mobile:", "X-Test: value"}})
	if h["user-agent"][0] != "custom-agent" || h["sec-ch-ua"][0] != "custom-hints" || h["x-test"][0] != "value" {
		t.Fatal("lost custom header overrides")
	}
	if _, ok := h["sec-ch-ua-mobile"]; ok {
		t.Fatal("did not remove Client Hint")
	}
	h = buildFetchHeaders(fetchOptions{userAgent: "custom-agent", headers: []string{"User-Agent: header-agent"}})
	if h["user-agent"][0] != "header-agent" {
		t.Fatal("-H must override -A")
	}
}

func TestFetchProfileReporting(t *testing.T) {
	var out bytes.Buffer
	printFetchProfiles(&out)
	if !strings.Contains(out.String(), "chrome-latest (Chrome-150;") {
		t.Fatalf("default version not reported: %s", out.String())
	}
	for _, name := range []string{"chrome_150", "chrome_152", "chrome_152_PSK"} {
		if !strings.Contains(out.String(), "  "+name+"\n") {
			t.Errorf("missing profile %s", name)
		}
	}
	if got := profileLabel(""); got != "Chrome-150" {
		t.Fatalf("status hides resolved default: %s", got)
	}
	if !strings.Contains(fetchClaudeMd(), "Chrome-150") {
		t.Fatal("generated reference does not identify the linked default")
	}
}

func TestNonChromeProfilesKeepHeaderOverrides(t *testing.T) {
	h := buildFetchHeaders(fetchOptions{profile: "firefox_148", userAgent: "custom-firefox", headers: []string{"Sec-CH-UA:", "Sec-CH-UA-Mobile:", "Sec-CH-UA-Platform:"}})
	if h["user-agent"][0] != "custom-firefox" {
		t.Fatal("lost non-Chrome user agent override")
	}
	for _, name := range []string{"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform"} {
		if _, ok := h[name]; ok {
			t.Errorf("could not remove %s", name)
		}
	}
}
