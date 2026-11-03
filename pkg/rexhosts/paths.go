// Package rexhosts reads and writes lazyrex-hosts.lua, the only file lazyrex
// writes in Rex's config directory.
package rexhosts

import (
	"fmt"
	"path/filepath"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// Paths locates lazyrex's file. Init names, for Status only, the file the
// server loads at start: lazyrex never reads or writes it.
type Paths struct{ Dir, Hosts, Init string }

// Locate finds Rex's config directory: $XDG_CONFIG_HOME/rex when that is
// absolute, else HOME/.config/rex. $REX_CONFIG never moves lazyrex's file,
// as the server may run without lazyrex's variables.
func Locate(getenv func(string) string, home func() (string, error)) (Paths, error) {
	var dir string
	if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		dir = filepath.Join(filepath.Clean(x), "rex")
	} else {
		h, err := home()
		switch {
		case err != nil:
			return Paths{}, fmt.Errorf("cannot find your home directory: %v", err)
		case !filepath.IsAbs(h):
			return Paths{}, fmt.Errorf("your home directory %q is not an absolute path", h)
		}
		dir = filepath.Join(h, ".config", "rex")
	}
	p := Paths{Dir: dir, Hosts: filepath.Join(dir, rex.HostsFileName), Init: filepath.Join(dir, "init.lua")}
	if rc := getenv("REX_CONFIG"); rc != "" {
		p.Init = "$REX_CONFIG (" + rc + ")"
	}
	return p, nil
}
