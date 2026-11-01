package rex_test

import (
	"context"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func outcome(t *testing.T, fr *rextest.FakeRunner, tr rex.Transport, cmd rex.Cmd) rex.Outcome {
	t.Helper()
	c := localClient(fr)
	c.Transport = tr
	out, err := c.Exec(context.Background(), cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStatusStoppedLocal(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("whoami").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("server", "status").Return("", rextest.FixtureString("stderr_server_not_running.txt"), 1)
	fr.On("version").Return(rextest.FixtureString("version.json"), "", 0)
	who := outcome(t, fr, rex.Local{}, cmds.Whoami())
	srv := outcome(t, fr, rex.Local{}, cmds.ServerStatus())
	ver := outcome(t, fr, rex.Local{}, cmds.Version())
	if !rex.IsNotRunning(srv) {
		t.Fatalf("server status exit 1 not recognized as not running")
	}
	st := rex.DeriveStatus(rex.Local{}, who, &srv, &ver)
	if st.State != rex.StateStopped || st.State.String() != "stopped" {
		t.Fatalf("state = %v", st.State)
	}
	if st.APIMismatch || st.APIRevision != 9 {
		t.Fatalf("revision = %d mismatch=%v", st.APIRevision, st.APIMismatch)
	}
}

func TestStatusNotRunningOnlyFromServerStatus(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("whoami").Return("", "Error: boom", 1)
	fr.On("server", "status").Return("", rextest.FixtureString("stderr_server_not_running.txt"), 1)
	who := outcome(t, fr, rex.Local{}, cmds.Whoami())
	srv := outcome(t, fr, rex.Local{}, cmds.ServerStatus())
	if st := rex.DeriveStatus(rex.Local{}, who, &srv, nil); st.State != rex.StateStopped {
		t.Fatalf("not running mapped to %v", st.State)
	}
}

func TestStatusOnline(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("whoami").Return(rextest.FixtureString("whoami.json"), "", 0)
	fr.On("server", "status").Return(rextest.FixtureString("server_status.json"), "", 0)
	who := outcome(t, fr, rex.Local{}, cmds.Whoami())
	srv := outcome(t, fr, rex.Local{}, cmds.ServerStatus())
	st := rex.DeriveStatus(rex.Local{}, who, &srv, nil)
	if st.State != rex.StateOnline || st.Whoami == nil || st.Server == nil || st.Whoami.Endpoint == "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusUnreachableRemote(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("whoami").Return("", "Error: no Rex server is running", 4)
	who := outcome(t, fr, rex.Native{Server: "alias"}, cmds.Whoami())
	if st := rex.DeriveStatus(rex.Native{Server: "alias"}, who, nil, nil); st.State != rex.StateUnreachable {
		t.Fatalf("state = %v", st.State)
	}
	fr.On("'whoami'").Return("", "ssh: connect to host remote-host port 22: Connection refused\nmore", 255)
	ssh := rex.SSHExec{Host: "remote-host"}
	who = outcome(t, fr, ssh, cmds.Whoami())
	st := rex.DeriveStatus(ssh, who, nil, nil)
	if st.State != rex.StateSSHUnreachable || st.State.String() != "unreachable (ssh)" || st.Detail != "ssh: connect to host remote-host port 22: Connection refused" {
		t.Fatalf("ssh status = %+v", st)
	}
}

func TestStatusAPIMismatch(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("whoami").Return(rextest.FixtureString("whoami.json"), "", 0)
	fr.On("server", "status").Return(`{"api_revision":10,"pid":1}`, "", 0)
	who := outcome(t, fr, rex.Local{}, cmds.Whoami())
	srv := outcome(t, fr, rex.Local{}, cmds.ServerStatus())
	st := rex.DeriveStatus(rex.Local{}, who, &srv, nil)
	if !st.APIMismatch || st.APIRevision != 10 {
		t.Fatalf("status = %+v", st)
	}
	fr.On("version").Return(`{"rex_version":"x","api_revision":8}`, "", 0)
	ver := outcome(t, fr, rex.Local{}, cmds.Version())
	fr.On("server", "status").Return(rextest.FixtureString("server_status.json"), "", 0)
	srv = outcome(t, fr, rex.Local{}, cmds.ServerStatus())
	if st := rex.DeriveStatus(rex.Local{}, who, &srv, &ver); !st.APIMismatch {
		t.Fatalf("local version revision 8 not flagged")
	}
}
