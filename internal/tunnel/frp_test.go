package tunnel

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestFrpConfigCarriesEveryFieldTheServerNeeds(t *testing.T) {
	f := NewFrp(FrpOptions{
		LocalPort:  8802,
		ServerAddr: "mcp.example.com",
		ServerPort: 7000,
		Token:      "s3cret",
		RemotePort: 18802,
		PublicBase: "https://mcp.example.com",
		ConfigDir:  t.TempDir(),
	})

	path, err := f.writeConfig()
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	text := string(data)

	// frp v0.52+ reads TOML only, and a missing key here fails silently on the
	// server side rather than at startup, so every one of these is asserted.
	for _, want := range []string{
		`serverAddr = "mcp.example.com"`,
		`serverPort = 7000`,
		`auth.token = "s3cret"`,
		`[[proxies]]`,
		`type = "tcp"`,
		`localIP = "127.0.0.1"`,
		`localPort = 8802`,
		`remotePort = 18802`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("config is missing %q\n--- generated ---\n%s", want, text)
		}
	}
}

func TestFrpRefusesToStartWithoutThePiecesItCannotGuess(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		opts FrpOptions
		says string
	}{
		{
			name: "no server address",
			opts: FrpOptions{LocalPort: 8802, ConfigDir: dir},
			says: "-frp-server",
		},
		{
			name: "no remote port",
			opts: FrpOptions{LocalPort: 8802, ServerAddr: "h", ConfigDir: dir},
			says: "-frp-remote",
		},
		{
			name: "no public url",
			opts: FrpOptions{LocalPort: 8802, ServerAddr: "h", RemotePort: 1, ConfigDir: dir},
			says: "-public-url",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := NewFrp(c.opts).Start(context.Background())
			if err == nil {
				t.Fatal("Start accepted an incomplete configuration")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the error should name %s, got: %v", c.says, err)
			}
		})
	}
}

func TestFrpSaysWhereToGetTheClient(t *testing.T) {
	f := NewFrp(FrpOptions{
		LocalPort: 8802, ServerAddr: "h", ServerPort: 7000,
		RemotePort: 1, PublicBase: "https://x", ConfigDir: t.TempDir(),
	})
	f.exe = "" // pretend nothing is installed
	err := f.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded with no client binary")
	}
	if !strings.Contains(err.Error(), "frpc") || !strings.Contains(err.Error(), "fatedier/frp") {
		t.Errorf("the error should name the binary and where to download it, got: %v", err)
	}
}

func TestFrpAddressIsTheHttpsOrigin(t *testing.T) {
	f := NewFrp(FrpOptions{PublicBase: "https://mcp.example.com/"})
	if got := f.publicURL(); got != "https://mcp.example.com" {
		t.Errorf("publicURL = %q, want the trailing slash trimmed", got)
	}
}

func TestFrpPublishesTheAddressOnceAndOnlyOnce(t *testing.T) {
	f := NewFrp(FrpOptions{PublicBase: "https://mcp.example.com"})
	if got := f.MCPURL("/mcp"); got != "" {
		t.Errorf("MCPURL before the proxy is up = %q, want empty", got)
	}

	var seen []string
	f.SetOnURL(func(u string) { seen = append(seen, u) })
	f.setURL("https://mcp.example.com")

	if got := f.MCPURL("/mcp"); got != "https://mcp.example.com/mcp" {
		t.Errorf("MCPURL = %q", got)
	}
	if len(seen) != 1 {
		t.Fatalf("OnURL fired %d times, want 1: %v", len(seen), seen)
	}

	// The panel re-renders on every callback, so an unchanged address must not
	// fire again.
	f.setURL("https://mcp.example.com")
	if len(seen) != 1 {
		t.Errorf("OnURL fired again for an unchanged address: %v", seen)
	}

	if got := f.Status().Provider; got != "frp" {
		t.Errorf("Status().Provider = %q, want frp", got)
	}
}
