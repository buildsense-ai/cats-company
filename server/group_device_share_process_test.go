package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type shareProcessCommand struct {
	Action    string
	Transport string
	RequestID string
	Wire      ServerMessage
}

type shareProcessReply struct {
	Grant        *ScopedDeviceGrant
	Wire         *ServerMessage
	Error        string
	Remote       bool
	PID          int
	Pending      bool
	DefaultEmpty bool
}

// Separate OS processes prevent a local *Client or Go map from hiding a
// cross-node regression. Client inboxes and permission storage are fixtures;
// Redis routing, pub/sub, authorization and RPC handlers are real code paths.
func TestGroupSharedRPCAcrossProcesses(t *testing.T) {
	redisURL := os.Getenv("CATS_GROUP_SHARE_TEST_REDIS_URL")
	if redisURL == "" {
		var closeRedis func()
		redisURL, closeRedis = newRedisRuntimeTestServer(t)
		defer closeRedis()
	}
	prefix := "sharing-process:" + randomDeviceGrantIDSuffix()
	if os.Getenv("CATS_GROUP_SHARE_TEST_REDIS_URL") != "" {
		cleanupState, err := NewRedisRuntimeState(context.Background(), RedisRuntimeOptions{URL: redisURL, KeyPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			defer cleanupState.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			iterator := cleanupState.client.Scan(ctx, 0, prefix+":*", 100).Iterator()
			for iterator.Next(ctx) {
				cleanupState.client.Del(ctx, iterator.Val())
			}
			if err := iterator.Err(); err != nil {
				t.Error("test namespace cleanup failed", err)
			}
		})
	}
	start := func(role string) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGroupSharedRPCNodeProcess$", "-test.timeout=45s")
		cmd.Env = append(os.Environ(), "CATS_SHARE_NODE_ROLE="+role, "CATS_SHARE_NODE_REDIS="+redisURL, "CATS_SHARE_NODE_PREFIX="+prefix)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := os.Create(filepath.Join(t.TempDir(), role+".log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = stdin.Close()
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case err := <-exited:
				if err != nil {
					t.Errorf("%s test node exited unsuccessfully: %v", role, err)
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-exited
				t.Errorf("%s test node did not shut down", role)
			}
			_ = stderr.Close()
			if t.Failed() {
				if raw, err := os.ReadFile(stderr.Name()); err == nil {
					t.Logf("%s node: %s", role, raw)
				}
			}
		})
		line := make(chan string, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			if scanner.Scan() {
				line <- scanner.Text()
			} else {
				line <- ""
			}
		}()
		select {
		case address := <-line:
			if address == "" {
				t.Fatal("node failed to start; inspect child log", role)
			}
			return address
		case <-time.After(15 * time.Second):
			t.Fatal("node startup timed out", role)
		}
		return ""
	}
	deviceNode, agentNode := start("device"), start("agent")
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(address string, command shareProcessCommand) shareProcessReply {
		raw, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(address, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var reply shareProcessReply
		if err = json.NewDecoder(response.Body).Decode(&reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	for _, mode := range []string{"default", "explicit"} {
		grantReply := call(agentNode, shareProcessCommand{Action: "grant", Transport: mode})
		if grantReply.Error != "" || grantReply.Grant == nil || !grantReply.Remote {
			t.Fatal("grant did not target a remote-only client", grantReply.Error)
		}
		if grantReply.PID == call(deviceNode, shareProcessCommand{Action: "pid"}).PID {
			t.Fatal("nodes share an OS process")
		}
		if mode == "explicit" && (!grantReply.DefaultEmpty || grantReply.Grant.TargetScope != "explicit_device") {
			t.Fatal("explicit grant silently selected a default device")
		}
		grant := grantReply.Grant
		for _, transport := range []string{"device", "thin"} {
			t.Run(mode+"/"+transport, func(t *testing.T) {
				id := "shared-process-" + mode + "-" + transport
				dispatched := call(agentNode, shareProcessCommand{Action: "dispatch", Transport: transport, RequestID: id})
				if dispatched.Error != "" {
					t.Fatal(dispatched.Error)
				}
				forwarded := call(deviceNode, shareProcessCommand{Action: "receive", RequestID: id})
				if forwarded.Wire == nil {
					t.Fatal("shared request never reached remote Connector", forwarded.Error)
				}
				wire := forwarded.Wire
				if transport == "device" {
					if wire.DeviceRPC == nil || wire.DeviceRPC.ActorUserID != "usr8" || wire.DeviceRPC.OwnerUserID != "usr8" || wire.DeviceRPC.ShareID != "" {
						t.Fatal("remote request did not use owner protocol")
					}
				} else if wire.ThinToolRPC == nil || wire.ThinToolRPC.ActorUserID != "usr8" || wire.ThinToolRPC.TargetOwnerUserID != "usr8" || wire.ThinToolRPC.ShareID != "" {
					t.Fatal("remote thin request did not use owner protocol")
				}
				if reply := call(deviceNode, shareProcessCommand{Action: "reply", Wire: *wire}); reply.Error != "" {
					t.Fatal(reply.Error)
				}
				returned := call(agentNode, shareProcessCommand{Action: "receive", RequestID: id})
				if returned.Wire == nil {
					t.Fatal("result did not return across nodes", returned.Error)
				}
				if transport == "device" {
					result := returned.Wire.DeviceRPC
					if result == nil || result.ActorUserID != "usr7" || result.OwnerUserID != "usr8" || result.SessionKey != grant.SessionKey {
						t.Fatal("result lost actual actor")
					}
				} else {
					result := returned.Wire.ThinToolRPC
					if result == nil || result.ActorUserID != "usr7" || result.TargetOwnerUserID != "usr8" || result.SessionKey != grant.SessionKey {
						t.Fatal("thin result lost actual actor")
					}
				}
				for _, node := range []string{agentNode, deviceNode} {
					if reply := call(node, shareProcessCommand{Action: "pending", RequestID: id}); reply.Pending {
						t.Fatal("completed cross-node request retained pending state")
					}
				}
			})
		}
		if reply := call(agentNode, shareProcessCommand{Action: "renew"}); reply.Error != "" {
			t.Fatal("remote grant renewal failed", reply.Error)
		}
	}
	call(agentNode, shareProcessCommand{Action: "revoke"})
	for _, transport := range []string{"device", "thin"} {
		id := "revoked-process-" + transport
		call(agentNode, shareProcessCommand{Action: "dispatch", Transport: transport, RequestID: id})
		if reply := call(deviceNode, shareProcessCommand{Action: "receive", RequestID: id}); reply.Wire != nil {
			t.Fatal("revoked grant reached remote device")
		}
	}
	if reply := call(agentNode, shareProcessCommand{Action: "renew"}); reply.Error == "" {
		t.Fatal("revoked remote grant renewed")
	}
}

func TestGroupSharedRPCNodeProcess(t *testing.T) {
	role := os.Getenv("CATS_SHARE_NODE_ROLE")
	if role == "" {
		t.Skip("subprocess helper")
	}
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	url, prefix := os.Getenv("CATS_SHARE_NODE_REDIS"), os.Getenv("CATS_SHARE_NODE_PREFIX")
	state, err := NewRedisRuntimeState(context.Background(), RedisRuntimeOptions{URL: url, KeyPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	auth, err := NewRedisDeviceAuthorizationStore(context.Background(), RedisDeviceAuthorizationOptions{URL: url, KeyPrefix: prefix + ":auth"})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	db := &groupSharingTestStore{identityMessageStore: &identityMessageStore{
		owners: map[int64]int64{42: 7}, users: map[int64]*types.User{7: {ID: 7, AccountType: types.AccountHuman}, 8: {ID: 8, AccountType: types.AccountHuman}, 42: {ID: 42, AccountType: types.AccountBot}},
		groupMembers: []*types.GroupMember{{GroupID: 1, UserID: 7, Role: "member"}, {GroupID: 1, UserID: 8, Role: "member"}, {GroupID: 1, UserID: 42, Role: "member"}},
	}, share: store.GroupDeviceShare{ID: "process-share", OwnerUID: 8, GranteeUID: 7, TopicID: "grp_1", DeviceID: "shared-pc", DeviceEpoch: 1, Operations: []string{"read_file"}, Status: "active", Revision: 1}}
	hub := newHubWithState(db, nil, state, "sharing-"+role, auth)
	peer := &Client{hub: hub, uid: 42, accountType: types.AccountBot, bodyID: "cloud", connectionID: "process-agent", send: make(chan []byte, 32)}
	if role == "device" {
		peer = &Client{hub: hub, uid: 8, accountType: types.AccountHuman, connectionID: "process-pc", send: make(chan []byte, 32)}
		device, err := hub.userDevices.register(8, RegisterUserDeviceRequest{DeviceID: "shared-pc", BodyID: "shared-pc", InstallationID: "shared-pc", RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file"}})
		if err != nil {
			t.Fatal(err)
		}
		hub.addClient(peer)
		hub.bindDeviceClient(8, device, peer)
	} else {
		peer.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability, groupDeviceShareCapability})
		hub.addClient(peer)
		hub.bindClientRuntimeRoute(peer)
		if _, err := hub.bodyLeases.acquire(42, "cloud", peer.connectionID); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool {
		counts, err := state.client.PubSubNumSub(context.Background(), state.nodeInboxChannel(hub.nodeID)).Result()
		return err == nil && counts[state.nodeInboxChannel(hub.nodeID)] > 0
	}, "node Redis inbox subscribed")
	var grant ScopedDeviceGrant
	stop := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command shareProcessCommand
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			http.Error(w, "invalid test command", 400)
			return
		}
		reply := shareProcessReply{PID: os.Getpid()}
		switch command.Action {
		case "grant":
			session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
			preference, err := hub.userDevices.selectionRecord(7, session)
			if err != nil {
				reply.Error = err.Error()
				break
			}
			deviceID, source, owner := "shared-pc", "test", int64(8)
			if command.Transport == "explicit" {
				deviceID, source, owner = "", deviceSelectionUnspecifiedSource, 7
			}
			if _, err := hub.userDevices.confirmDeviceSelection(7, session, deviceID, source, preference.Revision, owner); err != nil {
				reply.Error = err.Error()
				break
			}
			turn := hub.groupDeviceTurnContext(7, "grp_1", peer, nil, nil, true)
			grants := turn.Grants
			if command.Transport == "explicit" {
				grants = turn.ExplicitGrants
			}
			if len(grants) != 1 {
				reply.Error = "missing shared grant"
				break
			}
			grant = grants[0]
			reply.DefaultEmpty = turn.Selection != nil && turn.Selection.SelectedDevice == nil && turn.Selection.Status == DeviceSelectionNeedsSelection
			reply.Grant = &grant
			route, local := hub.findDeviceRPCTarget(8, UserDevice{DeviceID: "shared-pc"})
			reply.Remote = local == nil && route.NodeID != "" && route.NodeID != hub.nodeID && hub.routeConnected(route)
		case "dispatch":
			if command.Transport == "device" {
				hub.handleMessage(peer, &ClientMessage{DeviceRPC: &MsgDeviceRPC{Type: "request", RequestID: command.RequestID, GrantID: grant.GrantID, TargetScope: grant.TargetScope, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, DeviceID: grant.DeviceID, Operation: "read_file", ToolName: "read_file"}})
			} else {
				hub.handleMessage(peer, &ClientMessage{ThinToolRPC: &MsgThinToolRPC{Type: "request", RequestID: command.RequestID, GrantID: grant.GrantID, TargetScope: grant.TargetScope, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, TargetOwnerUserID: grant.OwnerUserID, TargetDeviceID: grant.DeviceID, ToolName: "read_file"}})
			}
		case "receive":
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
		receive:
			for {
				select {
				case raw := <-peer.send:
					var wire ServerMessage
					if json.Unmarshal(raw, &wire) != nil {
						continue
					}
					if (wire.DeviceRPC != nil && wire.DeviceRPC.RequestID == command.RequestID) || (wire.ThinToolRPC != nil && wire.ThinToolRPC.RequestID == command.RequestID) {
						reply.Wire = &wire
						break receive
					}
				case <-timer.C:
					reply.Error = "no RPC received"
					break receive
				}
			}
		case "reply":
			if wire := command.Wire.DeviceRPC; wire != nil {
				wire.Type = "result"
				wire.Result = map[string]any{"ok": true, "content": "remote-pc"}
				hub.handleMessage(peer, &ClientMessage{DeviceRPC: wire})
			} else if wire := command.Wire.ThinToolRPC; wire != nil {
				wire.Type = "result"
				wire.Result = map[string]any{"ok": true, "content": "remote-pc"}
				hub.handleMessage(peer, &ClientMessage{ThinToolRPC: wire})
			}
		case "renew":
			next, err := hub.renewDeviceGrant(peer, &MsgDeviceGrantRenewal{Type: "request", RequestID: "remote-renew", GrantID: grant.GrantID})
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.Grant = &next
			}
		case "revoke":
			db.share.Status = "revoked"
			db.share.Revision++
		case "pending":
			_, devicePending := hub.deviceRPC.get(command.RequestID)
			_, thinPending := hub.thinToolRPC.get(command.RequestID)
			reply.Pending = devicePending || thinPending
		case "pid":
		default:
			reply.Error = "unknown test action"
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer api.Close()
	// Only the endpoint is sent to stdout; operational logs remain on stderr.
	fmt.Fprintln(os.Stdout, api.URL)
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(stop) }()
	<-stop
}
