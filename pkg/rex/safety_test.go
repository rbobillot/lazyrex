package rex_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const self = rex.BlockID("block:01self")

func policyClient(t *testing.T, tr rex.Transport, allow ...string) (*rex.Client, *rextest.FakeRunner) {
	t.Helper()
	p, warnings := rex.NewPolicy(string(self), allow)
	if p.SelfBlock != self {
		t.Fatalf("self block = %q (warnings %v)", p.SelfBlock, warnings)
	}
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	return &rex.Client{Runner: fr, Transport: tr, Binary: "rex", Timeout: 10 * time.Second, Policy: p}, fr
}

func assertDenied(t *testing.T, c *rex.Client, fr *rextest.FakeRunner, cmd rex.Cmd, label string) {
	t.Helper()
	if d := c.Decide(cmd); d.Verdict != rex.Deny {
		t.Errorf("%s: verdict %v, want Deny", label, d.Verdict)
	}
	before := len(fr.Calls())
	_, err := c.Exec(context.Background(), cmd, true)
	var denied *rex.DeniedError
	if !errors.As(err, &denied) {
		t.Errorf("%s: Exec error %v, want DeniedError", label, err)
	}
	var confirm *rex.NeedsConfirmError
	if errors.As(err, &confirm) {
		t.Errorf("%s: a denied operation offered a confirmation", label)
	}
	if len(fr.Calls()) != before {
		t.Errorf("%s: runner called", label)
	}
}

func assertConfirm(t *testing.T, c *rex.Client, fr *rextest.FakeRunner, cmd rex.Cmd, label string) {
	t.Helper()
	if d := c.Decide(cmd); d.Verdict != rex.Confirm {
		t.Errorf("%s: verdict %v (%s), want Confirm", label, d.Verdict, d.Reason)
		return
	}
	before := len(fr.Calls())
	_, err := c.Exec(context.Background(), cmd, false)
	var confirm *rex.NeedsConfirmError
	if !errors.As(err, &confirm) {
		t.Errorf("%s: unconfirmed Exec error %v, want NeedsConfirmError", label, err)
	}
	if confirm != nil && confirm.Display != c.Display(cmd) {
		t.Errorf("%s: confirm shows %q, want %q", label, confirm.Display, c.Display(cmd))
	}
	if len(fr.Calls()) != before {
		t.Errorf("%s: runner called before confirmation", label)
	}
	if _, err := c.Exec(context.Background(), cmd, true); err != nil {
		t.Errorf("%s: confirmed Exec failed: %v", label, err)
	}
	if len(fr.Calls()) != before+1 {
		t.Errorf("%s: confirmed Exec made %d calls", label, len(fr.Calls())-before)
	}
}

func TestSelfBlockRefused(t *testing.T) {
	for _, tr := range []rex.Transport{rex.Local{}, rex.Native{Server: "alias"}, rex.SSHExec{Host: "remote-host"}} {
		c, fr := policyClient(t, tr)
		for _, id := range []rex.BlockID{self, "BLOCK:01SELF"} {
			assertDenied(t, c, fr, cmds.Send(S, id, "x"), tr.Name()+" send")
			assertDenied(t, c, fr, cmds.SendKey(S, id, "enter"), tr.Name()+" send-key")
			assertDenied(t, c, fr, cmds.SendKey(S, id, "ctrl+c"), tr.Name()+" interrupt")
			assertDenied(t, c, fr, cmds.BlockClose(S, id), tr.Name()+" close")
		}
	}
}

func TestSelfBlockContainers(t *testing.T) {
	c, fr := policyClient(t, rex.Local{})
	assertDenied(t, c, fr, cmds.Kill(S).Touching("block:01b1", self), "kill containing self")
	assertDenied(t, c, fr, cmds.WindowClose(S, W).Touching(self), "close window containing self")
	assertConfirm(t, c, fr, cmds.Kill(S).Touching("block:01b1"), "kill without self")
}

func TestMalformedSelfBlockIgnored(t *testing.T) {
	for _, env := range []string{"abcd", "01self", "block:", "block:01 self", "-block:01self", "window:01self"} {
		p, warnings := rex.NewPolicy(env, nil)
		if p.SelfBlock != "" {
			t.Errorf("REX_BLOCK %q accepted as %q", env, p.SelfBlock)
		}
		if len(warnings) == 0 {
			t.Errorf("REX_BLOCK %q ignored without a warning", env)
		}
		if p.IsSelf("block:01self") || p.IsSelf("block:01abcd") {
			t.Errorf("REX_BLOCK %q matched by suffix", env)
		}
	}
	if p, warnings := rex.NewPolicy("", nil); p.SelfBlock != "" || len(warnings) != 0 {
		t.Errorf("empty REX_BLOCK: %q %v", p.SelfBlock, warnings)
	}
}

func TestConfirmBeforeRunKinds(t *testing.T) {
	c, fr := policyClient(t, rex.Local{})
	assertConfirm(t, c, fr, cmds.Kill(S), "kill")
	assertConfirm(t, c, fr, cmds.WindowClose(S, W), "window close")
	assertConfirm(t, c, fr, cmds.BlockClose(S, B), "block close")
	for _, k := range []string{"ctrl+c", "ctrl+d", `ctrl+\`, "ctrl+z", "CTRL+C"} {
		assertConfirm(t, c, fr, cmds.SendKey(S, B, k), "send-key "+k)
	}
	assertConfirm(t, c, fr, cmds.ServerStop(), "server stop")
	assertConfirm(t, c, fr, cmds.ServerStart(), "server start")
	for _, cmd := range []rex.Cmd{cmds.SendKey(S, B, "enter"), cmds.Send(S, B, "ls"), cmds.Ls(), cmds.Capture(S, B, "vt"), cmds.Split(S, B, rex.SplitOpts{Dir: "right"})} {
		if d := c.Decide(cmd); d.Verdict != rex.Allow {
			t.Errorf("%q: verdict %v, want Allow", cmd.Args, d.Verdict)
		}
	}
}

func TestPaletteConfirm(t *testing.T) {
	c, fr := policyClient(t, rex.Local{})
	assertConfirm(t, c, fr, cmds.APICall("", "server.stop", "{}"), "server.stop")
	assertConfirm(t, c, fr, cmds.APICall(S, "session.close_block", `{"block_id":"block:01b1"}`), "close_block")
	assertConfirm(t, c, fr, cmds.APICall(S, "block.send_input", `{"text":"x"}`), "send input")
	assertConfirm(t, c, fr, cmds.APICall("", "server.config_reload", "{}"), "config_reload")
	assertConfirm(t, c, fr, cmds.Do("mystery"), "unknown do")
	assertConfirm(t, c, fr, cmds.ClientDo(C, "closeWindow"), "client closeWindow")
	assertConfirm(t, c, fr, cmds.BlockCall(S, B, "reset"), "unknown block method")
	assertDenied(t, c, fr, cmds.Do("x", "b="+string(self)), "do naming own block")
	assertDenied(t, c, fr, cmds.APICall(S, "session.view", `{"block_id":"BLOCK:01SELF"}`), "api naming own block")
	assertDenied(t, c, fr, cmds.DoJSON("x", `{"b":"block:01self"}`), "do --args naming own block")
	assertDenied(t, c, fr, cmds.BlockCall(S, self, "reset"), "block call on own block")
	for _, name := range []string{"session.view", "session.list", "target.complete", "hosts.list", "server.status"} {
		if d := c.Decide(cmds.APICall("", name, "{}")); d.Verdict != rex.Allow {
			t.Errorf("%s: verdict %v, want Allow", name, d.Verdict)
		}
	}
	if d := c.Decide(cmds.Raw("frobnicate")); d.Verdict != rex.Confirm {
		t.Errorf("raw unknown command: verdict %v", d.Verdict)
	}
}

func TestDenied(t *testing.T) {
	c, fr := policyClient(t, rex.Local{})
	for _, m := range []string{"hosts.put", "hosts.remove", "server.tailscale.enable", "server.tailscale.login",
		"server.pair_device", "server.listen_add", "server.apple.grant_file_access"} {
		assertDenied(t, c, fr, cmds.APICall("", m, "{}"), "api "+m)
	}
	for _, args := range [][]string{
		{"server", "start", "--extra-listen=tcp://0.0.0.0:1"},
		{"server", "start", "--extra-listen", "x"},
		{"server", "run", "--allow-anonymous"},
		{"server", "start", "--tailscale"},
		{"server", "start", "--tailscale-hostname=x"},
		{"server", "tailscale", "enable"},
		{"hosts", "add", "h"},
		{"hosts", "edit", "h"},
		{"hosts", "remove", "h"},
		{"-S", "alias", "hosts", "add", "h"},
	} {
		assertDenied(t, c, fr, cmds.Raw(args...), "raw "+args[0]+" "+args[1])
	}
	for _, name := range []string{"pairDevice", "install_update", "updateNow", "performUpdate", "checkForUpdates", "unpair", "listenerAdd", "upgrade"} {
		assertDenied(t, c, fr, cmds.Do(name), "do "+name)
		assertDenied(t, c, fr, cmds.ClientDo(C, name), "client action "+name)
		if d := c.Policy.ClassifyName(name); d.Verdict != rex.Deny {
			t.Errorf("ClassifyName(%s) = %v", name, d.Verdict)
		}
	}
}

func TestAllowListLimits(t *testing.T) {
	c, fr := policyClient(t, rex.Local{}, "hosts.put", "server.pair_device", "server.listen_add", "server.tailscale.enable", "checkForUpdates")
	for _, m := range []string{"hosts.put", "server.pair_device", "server.listen_add", "server.tailscale.enable"} {
		assertDenied(t, c, fr, cmds.APICall("", m, "{}"), "allow-listed "+m)
	}
	assertConfirm(t, c, fr, cmds.Do("checkForUpdates"), "allow-listed checkForUpdates")
	assertConfirm(t, c, fr, cmds.ClientDo(C, "checkForUpdates"), "allow-listed client checkForUpdates")
	ignored := c.Policy.IgnoredAllow()
	slices.Sort(ignored)
	want := []string{"hosts.put", "server.listen_add", "server.pair_device", "server.tailscale.enable"}
	if !slices.Equal(ignored, want) {
		t.Fatalf("IgnoredAllow = %v", ignored)
	}
	_, warnings := rex.NewPolicy("", []string{"hosts.put"})
	if len(warnings) == 0 {
		t.Fatalf("ignored allow entry without a warning")
	}
}

func TestNotDenied(t *testing.T) {
	c, _ := policyClient(t, rex.Local{})
	for _, name := range []string{"hosts.list", "update_status", "getUpdateInfo", "ghostscript_view", "server.whoami", "server.apple.privacy_status"} {
		if d := c.Policy.ClassifyName(name); d.Verdict == rex.Deny {
			t.Errorf("%s denied: %s", name, d.Reason)
		}
	}
	if d := c.Policy.ClassifyName("hosts.list"); d.Verdict != rex.Allow {
		t.Errorf("hosts.list verdict %v", d.Verdict)
	}
}

func TestSelfMentioned(t *testing.T) {
	c, fr := policyClient(t, rex.Local{})
	c.Policy.Labels.Set("  Shell ")
	denied := map[string]rex.Cmd{
		"full id":             cmds.APICall("", "session.view", `{"x":"block:01self"}`),
		"escaped json":        cmds.APICall("", "session.view", `{"b":"block\u003a01self"}`),
		"escaped key":         cmds.APICall("", "session.view", `{"block\u003a01self":1}`),
		"nested":              cmds.APICall("", "session.view", `{"a":[{"b":"BLOCK:01SELF"}]}`),
		"suffix in json":      cmds.APICall("", "session.view", `{"x":"01self"}`),
		"kv suffix":           cmds.Do("mystery", "b=01SELF"),
		"kv short suffix":     cmds.Do("mystery", "b=self"),
		"kv json value":       cmds.Do("mystery", `b="block\u003a01self"`),
		"kv label":            cmds.Do("mystery", "b=shell"),
		"kv label case":       cmds.Do("mystery", "b= SHELL "),
		"json label":          cmds.APICall("", "session.view", `{"t":"shell"}`),
		"whole payload label": cmds.APICall("", "session.view", `"shell"`),
		"args label":          cmds.DoJSON("mystery", `{"t":"Shell"}`),
		"client label":        cmds.ClientDoJSON(C, "openFile", `{"path":"shell"}`),
	}
	for name, cmd := range denied {
		assertDenied(t, c, fr, cmd, name)
	}
	allowed := map[string]rex.Cmd{
		"three chars":     cmds.Do("mystery", "b=elf"),
		"label substring": cmds.Do("mystery", "b=shellfish"),
		"other id":        cmds.APICall("", "session.view", `{"x":"block:01b1"}`),
		"plain":           cmds.APICall("", "session.view", `{"x":"hello"}`),
	}
	for name, cmd := range allowed {
		if d := c.Decide(cmd); d.Verdict == rex.Deny {
			t.Errorf("%s: denied: %s", name, d.Reason)
		}
	}
	c.Policy.Labels.Set("sh2")
	if d := c.Decide(cmds.Do("mystery", "b=shell")); d.Verdict == rex.Deny {
		t.Errorf("old label still refused after Set: %s", d.Reason)
	}
	if d := c.Decide(cmds.Do("mystery", "b=sh2")); d.Verdict != rex.Deny || !strings.Contains(d.Reason, string(self)) {
		t.Errorf("new label: %v %s", d.Verdict, d.Reason)
	}
}

// TestOwnBlockTargetKeys: a block-bearing argument must be a full block ID, so
// a label or suffix resolved by Rex can never reach the own block.
func TestOwnBlockTargetKeys(t *testing.T) {
	c, fr := policyClient(t, rex.Local{})
	for _, key := range []string{"block", "block_id", "pane", "pane_id", "target", "anchor", "from_block", "anchor_block_id", "left_pane"} {
		assertDenied(t, c, fr, cmds.Do("mystery", key+"=vim"), key+" label")
		assertDenied(t, c, fr, cmds.Do("mystery", key+"=01b1"), key+" suffix")
		assertDenied(t, c, fr, cmds.Do("mystery", key+"="+string(self)), key+" own block")
		assertDenied(t, c, fr, cmds.APICall(S, "session.view", `{"`+key+`":"vim"}`), key+" json label")
		assertDenied(t, c, fr, cmds.APICall(S, "session.view", `{"`+key+`":3}`), key+" json number")
		assertDenied(t, c, fr, cmds.APICall(S, "session.view", `{"a":{"`+key+`":"BLOCK:01SELF"}}`), key+" nested own block")
		if d := c.Decide(cmds.Do("mystery", key+"=block:01b1")); d.Verdict == rex.Deny {
			t.Errorf("%s with a full ID denied: %s", key, d.Reason)
		}
		if d := c.Decide(cmds.APICall(S, "session.view", `{"`+key+`":"block:01b1"}`)); d.Verdict == rex.Deny {
			t.Errorf("%s json full ID denied: %s", key, d.Reason)
		}
	}
	d := c.Decide(cmds.Do("mystery", "block_id=vim"))
	if !strings.Contains(d.Reason, "full block ID") {
		t.Errorf("reason %q does not ask for the full block ID", d.Reason)
	}
	for _, key := range []string{"session_id", "blocks", "label"} {
		if d := c.Decide(cmds.Do("mystery", key+"=vim")); d.Verdict == rex.Deny {
			t.Errorf("%s=vim denied: %s", key, d.Reason)
		}
	}
}

// TestTargetKeysWithoutSelf: block-bearing arguments need a full block ID
// even when lazyrex does not know its own block.
func TestTargetKeysWithoutSelf(t *testing.T) {
	p, _ := rex.NewPolicy("", nil)
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	c := &rex.Client{Runner: fr, Transport: rex.Local{}, Binary: "rex", Timeout: 10 * time.Second, Policy: p}
	for _, key := range []string{"block", "block_id", "pane", "pane_id", "target", "anchor", "from_block", "anchor_block_id", "left_pane"} {
		assertDenied(t, c, fr, cmds.Do("mystery", key+"=shell"), key+" label")
		assertDenied(t, c, fr, cmds.Do("mystery", key+"=01b1"), key+" suffix")
		assertDenied(t, c, fr, cmds.APICall(S, "session.view", `{"`+key+`":"shell"}`), key+" json label")
		assertDenied(t, c, fr, cmds.APICall(S, "session.view", `{"a":[{"`+key+`":7}]}`), key+" nested json number")
		assertDenied(t, c, fr, cmds.DoJSON("mystery", `{"`+key+`":"shell"}`), key+" --args label")
		assertDenied(t, c, fr, cmds.ClientDoJSON("client:02app", "pane.zoom", `{"`+key+`":"shell"}`), key+" client --args label")
		for _, ok := range []rex.Cmd{cmds.Do("mystery", key+"=block:01b1"), cmds.APICall(S, "session.view", `{"`+key+`":"block:01b1"}`)} {
			if d := c.Decide(ok); d.Verdict == rex.Deny {
				t.Errorf("%s with a full ID denied without $REX_BLOCK: %s", key, d.Reason)
			}
		}
	}
	if d := c.Decide(cmds.Do("mystery", "block_id=shell")); !strings.Contains(d.Reason, "full block ID") {
		t.Errorf("reason %q does not ask for the full block ID", d.Reason)
	}
	for _, v := range []string{"shell", "01b1", "self"} {
		if d := c.Decide(cmds.Do("mystery", "b="+v)); d.Verdict == rex.Deny {
			t.Errorf("b=%s denied without $REX_BLOCK: %s", v, d.Reason)
		}
	}
}

func TestNameTokens(t *testing.T) {
	cases := map[string][]string{
		"checkForUpdates":   {"check", "for", "updates"},
		"getUpdateInfo":     {"get", "update", "info"},
		"HTTPServer":        {"http", "server"},
		"ghostscript_view":  {"ghostscript", "view"},
		"a.b-c/d e":         {"a", "b", "c", "d", "e"},
		"server.listen_add": {"server", "listen", "add"},
		"pairDevice2":       {"pair", "device", "2"},
	}
	for in, want := range cases {
		if got := rex.NameTokens(in); !slices.Equal(got, want) {
			t.Errorf("NameTokens(%q) = %q, want %q", in, got, want)
		}
	}
}
