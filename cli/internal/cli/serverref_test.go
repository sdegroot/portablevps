package cli

import "testing"

func TestServerRef(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare name unchanged", "hetzner-nbg1-20260707a", "hetzner-nbg1-20260707a"},
		{"relative path", "servers/hetzner-nbg1-20260707a.nix", "hetzner-nbg1-20260707a"},
		{"dot-relative path", "./servers/hetzner-nbg1-20260707a.nix", "hetzner-nbg1-20260707a"},
		{"absolute path", "/home/op/epistola/servers/leaseweb-ams-20260704a.nix", "leaseweb-ams-20260704a"},
		{"bare filename with ext", "hetzner-fsn-20260710a.nix", "hetzner-fsn-20260710a"},
		{"trailing-slash dir ignored", "servers/", "servers"},
		{"empty stays empty", "", ""},
	}
	for _, c := range cases {
		if got := serverRef(c.in); got != c.want {
			t.Errorf("%s: serverRef(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
