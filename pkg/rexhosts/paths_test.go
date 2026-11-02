package rexhosts

import (
	"errors"
	"strings"
	"testing"
)

// TestLocate (H14, M-B5.2): lazyrex's file sits in Rex's config directory,
// never next to $REX_CONFIG, and never under a relative home.
func TestLocate(t *testing.T) {
	type env map[string]string
	home := func(h string, err error) func() (string, error) { return func() (string, error) { return h, err } }
	for _, tc := range []struct {
		name      string
		env       env
		home      func() (string, error)
		dir, init string
		errPart   string
	}{
		{"xdg absolute", env{"XDG_CONFIG_HOME": "/x/cfg"}, home("/home/u", nil), "/x/cfg/rex", "/x/cfg/rex/init.lua", ""},
		{"xdg trailing slash", env{"XDG_CONFIG_HOME": "/x/cfg/"}, home("/home/u", nil), "/x/cfg/rex", "/x/cfg/rex/init.lua", ""},
		{"xdg unset", env{}, home("/home/u", nil), "/home/u/.config/rex", "/home/u/.config/rex/init.lua", ""},
		{"xdg relative", env{"XDG_CONFIG_HOME": "cfg"}, home("/home/u", nil), "/home/u/.config/rex", "/home/u/.config/rex/init.lua", ""},
		{"xdg absolute, no home", env{"XDG_CONFIG_HOME": "/x/cfg"}, home("", errors.New("no home")), "/x/cfg/rex", "/x/cfg/rex/init.lua", ""},
		{"rex config absolute", env{"REX_CONFIG": "/etc/rex.lua"}, home("/home/u", nil), "/home/u/.config/rex", "$REX_CONFIG (/etc/rex.lua)", ""},
		{"rex config relative", env{"REX_CONFIG": "my.lua"}, home("/home/u", nil), "/home/u/.config/rex", "$REX_CONFIG (my.lua)", ""},
		{"home empty", env{}, home("", nil), "", "", "not an absolute path"},
		{"home relative", env{"XDG_CONFIG_HOME": "cfg"}, home("rel/home", nil), "", "", "not an absolute path"},
		{"home error", env{}, home("", errors.New("$HOME is not defined")), "", "", "$HOME is not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Locate(func(k string) string { return tc.env[k] }, tc.home)
			if tc.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errPart) {
					t.Fatalf("Locate = %+v, %v, want an error with %q", p, err, tc.errPart)
				}
				return
			}
			if err != nil || p.Dir != tc.dir || p.Hosts != tc.dir+"/lazyrex-hosts.lua" || p.Init != tc.init {
				t.Fatalf("Locate = %+v, %v, want dir %s, init %s", p, err, tc.dir, tc.init)
			}
		})
	}
}
