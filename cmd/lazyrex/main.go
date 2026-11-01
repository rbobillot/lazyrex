// Command lazyrex is a terminal UI for the Rex multiplexer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/gui"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("lazyrex", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "config file (default $XDG_CONFIG_HOME/lazyrex/config.yml or ~/.config/lazyrex/config.yml)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	var server string
	const serverUsage = "start on TARGET: a config target name, local, or a rex -S server (rex hosts label, host:port or URL)"
	fs.StringVar(&server, "server", "", serverUsage)
	fs.StringVar(&server, "S", "", "short for --server")
	sshHost := fs.String("ssh", "", "start on the SSH target reaching HOST (a ~/.ssh/config Host or user@host), added if none does")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: lazyrex [--server|-S TARGET | --ssh HOST] [--config PATH] [--version]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lazyrex: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *showVersion {
		fmt.Println("lazyrex " + buildVersion())
		return 0
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	sshSet := set["ssh"]
	if sshSet && (set["server"] || set["S"]) {
		fmt.Fprintln(os.Stderr, "lazyrex: --ssh and --server are exclusive")
		return 2
	}

	path, explicit := *cfgPath, *cfgPath != ""
	if !explicit {
		path = config.DefaultPath()
	}
	cfg, file, err := config.LoadFile(path, explicit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lazyrex:", err)
		return 2
	}
	targets, err := gui.ConfigTargets(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lazyrex: %s: %v\n", path, err)
		return 2
	}
	all, idx, err := rex.WithFlag(targets, server)
	if sshSet {
		all, idx, err = rex.WithSSHFlag(targets, *sshHost)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lazyrex:", err)
		return 2
	}
	var flagTarget *rex.Target
	if idx >= len(targets) {
		flagTarget = &all[idx]
	}
	bindings, err := gui.ResolveBindings(cfg.Keybindings)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lazyrex:", err)
		return 2
	}
	policy, warnings := rex.NewPolicy(os.Getenv("REX_BLOCK"), cfg.Safety.AllowActions)
	client := &rex.Client{
		Runner:    rex.ExecRunner{},
		Transport: rex.Local{},
		Binary:    cfg.Rex.Binary,
		Timeout:   cfg.Rex.Timeout,
		Policy:    policy,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ssh := &rex.SSHEnv{
		Dir:     sync.OnceValues(func() (string, error) { return rex.PrepareRuntimeDir(os.Getenv("XDG_RUNTIME_DIR")) }),
		Sockets: rex.OSSockets{},
		PID:     os.Getpid(),
		Probes:  &rex.SSHProbes{},
		Min:     time.Second,
		Max:     30 * time.Second,
	}
	var sshHosts []string
	if home, err := os.UserHomeDir(); err == nil {
		sshHosts = config.ReadSSHConfigHosts(filepath.Join(home, ".ssh", "config"))
	}
	o := gui.RunOptions{Client: client, Config: cfg, Bindings: bindings, Warnings: warnings,
		Targets: targets, Initial: all[idx].Name, Flag: flagTarget, File: file, SSH: ssh, SSHConfigHosts: sshHosts}
	if err := gui.Run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "lazyrex:", err)
		return 1
	}
	return 0
}
