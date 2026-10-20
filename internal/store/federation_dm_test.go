package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func dmStore(t *testing.T) (*Store, string, protocol.Room) {
	t.Helper()
	s, path := federationTestStore(t)
	enableFederationTest(t, s, 100, 3)
	if err := s.ConfigureFederationDM(context.Background(), "remote-node", []FederationDMConversation{dmConfig(1)}); err != nil {
		t.Fatal(err)
	}
	room, err := s.OpenFederatedDM(context.Background(), "local", "conversation", 1)
	if err != nil {
		t.Fatal(err)
	}
	return s, path, room
}
func dmConfig(epoch int64) FederationDMConversation {
	return FederationDMConversation{ConversationID: "conversation", LinkID: "dm-link", Epoch: epoch, LocalAgentID: "local", RemoteAgentID: "remote-agent"}
}
func dmEnvelope() FederationDMEnvelope {
	return FederationDMEnvelope{Version: 2, Type: "federated_dm", OriginNodeID: "remote-node", OriginMessageID: protocol.NewID(), ConversationID: "conversation", LinkID: "dm-link", Epoch: 1, RecipientAgentID: "local", Sender: protocol.AgentRef{ID: "remote-agent", Name: "RemoteName", Kind: "human"}, Text: "private text", SentAt: time.Now().UTC()}
}
func incomingDM(t *testing.T, s *Store) FederationDMEnvelope {
	t.Helper()
	e := dmEnvelope()
	var id string
	if err := s.db.QueryRow(`SELECT node_id FROM federation_node`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.RecipientNodeID = id
	return e
}

func TestFederatedDMProvisionScopeAndTransactionalQueue(t *testing.T) {
	s, _, room := dmStore(t)
	ctx := context.Background()
	if room.Kind != "federated_dm" {
		t.Fatal(room)
	}
	sent, err := s.SendMessage(ctx, "local", room.ID, protocol.SendMessageRequest{Text: "private", ClientMsgID: "once"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SendMessage(ctx, "local", room.ID, protocol.SendMessageRequest{Text: "private", ClientMsgID: "once"}, nil)
	if err != nil || !again.Replayed || again.Message.ID != sent.Message.ID {
		t.Fatal(again, err)
	}
	job, err := s.ClaimFederationDMJob(ctx, time.Now(), time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	var e FederationDMEnvelope
	json.Unmarshal([]byte(job.Payload), &e)
	if !s.FederationDMJobAuthorized(ctx, *job, e) || e.RecipientAgentID != "remote-agent" || e.Text != "private" {
		t.Fatal(e)
	}
	for _, table := range []string{"federation_outbox", "federation_file_outbox"} {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		if n != 0 {
			t.Fatal("DM exported through other protocol")
		}
	}
	s.federation.MaxOutbox = 1
	if _, err = s.SendMessage(ctx, "local", room.ID, protocol.SendMessageRequest{Text: "overflow"}, nil); !errors.Is(err, ErrFederationFull) {
		t.Fatal("missing backpressure", err)
	}
	messages, _, _ := s.History(ctx, "local", room.ID, 0, 0, 10)
	if len(messages) != 1 {
		t.Fatal("failed outbox leaked local DM")
	}
	for _, req := range []protocol.SendMessageRequest{{Text: "file", Files: []string{"unknown"}}, {Text: "activity", Meta: protocol.MessageMeta{Activity: &protocol.Activity{RunID: "run", Phase: "working", Title: "work"}}}, {Text: "reply", ReplyTo: sent.Message.ID}, {Text: "direct recipient", To: []string{"other"}}} {
		if _, err = s.SendMessage(ctx, "local", room.ID, req, nil); err == nil {
			t.Fatal("nontext DM accepted")
		}
	}
	if _, err = s.CreateInvite(ctx, "invite-third", Invite{Room: room.ID, Uses: 1, TTL: time.Hour}, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatal("DM invite allowed", err)
	}
	if err = s.CreateRoom(ctx, protocol.Room{ID: "fake-dm", Kind: "federated_dm"}, []string{"local"}, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatal("arbitrary federated room allowed", err)
	}
	var bridge string
	s.db.QueryRow(`SELECT bridge_agent_id FROM federation_dm_conversations`).Scan(&bridge)
	if _, err = s.RotateAgentToken(ctx, bridge, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatal("bridge token rotation", err)
	}
	if _, err = s.AgentByToken(ctx, bridge); err == nil {
		t.Fatal("bridge ID is login token")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE agent_id=?`, bridge).Scan(&n)
	if n != 1 {
		t.Fatal("bridge outside scoped room")
	}
	if err = s.CreateRoom(ctx, protocol.Room{ID: "bridge-invite", Kind: "dm"}, []string{"local", bridge}, "local"); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped bridge added to another room", err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.ID == bridge {
			t.Fatal("private conversation bridge leaked through directory")
		}
	}
}

func TestFederatedDMInboundIdentityIdempotencyAndNoLoop(t *testing.T) {
	s, _, room := dmStore(t)
	ctx := context.Background()
	e := incomingDM(t, s)
	var wg sync.WaitGroup
	ids := sync.Map{}
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			r, err := s.ImportFederationDM(ctx, "remote-node", e)
			if err != nil {
				t.Error(err)
			}
			ids.Store(r.LocalMessageID, true)
		})
	}
	wg.Wait()
	n := 0
	ids.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatal("duplicate imported")
	}
	messages, _, err := s.History(ctx, "local", room.ID, 0, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Meta.Federation.ConversationID != "conversation" || messages[0].From.Kind != "federation" {
		t.Fatal(messages, err)
	}
	var bridge, nodeBridge string
	s.db.QueryRow(`SELECT bridge_agent_id FROM federation_dm_conversations`).Scan(&bridge)
	s.db.QueryRow(`SELECT bridge_agent_id FROM federation_node`).Scan(&nodeBridge)
	if messages[0].From.ID != bridge || bridge == nodeBridge {
		t.Fatal("shared bridge used for DM")
	}
	for _, change := range []func(*FederationDMEnvelope){func(e *FederationDMEnvelope) { e.Text = "changed" }, func(e *FederationDMEnvelope) { e.Sender.ID = "third" }, func(e *FederationDMEnvelope) { e.RecipientAgentID = "third" }, func(e *FederationDMEnvelope) { e.RecipientNodeID = "wrong" }, func(e *FederationDMEnvelope) { e.Epoch = 2 }, func(e *FederationDMEnvelope) { e.LinkID = "other" }, func(e *FederationDMEnvelope) { e.ConversationID = "other" }} {
		bad := e
		change(&bad)
		if _, err = s.ImportFederationDM(ctx, "remote-node", bad); err == nil {
			t.Fatal("wrong binding/changed payload accepted", bad)
		}
	}
	if _, err = s.ImportFederationDM(ctx, "unregistered", e); err == nil {
		t.Fatal("unregistered peer accepted")
	}
	if j, err := s.ClaimFederationDMJob(ctx, time.Now(), time.Minute); err != nil || j != nil {
		t.Fatal("remote DM looped", j, err)
	}
	v1 := FederationEnvelope{Version: 1, OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID, SourceRoom: "general", DestinationRoom: "general", Sender: e.Sender, Text: e.Text, SentAt: e.SentAt}
	if _, err = s.ImportFederation(ctx, "remote-node", v1); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("v1 origin collision", err)
	}
}

func TestFederatedDMACLRetirementAndNoHistoryInheritance(t *testing.T) {
	s, _, room := dmStore(t)
	ctx := context.Background()
	e := incomingDM(t, s)
	if _, err := s.ImportFederationDM(ctx, "remote-node", e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvite(ctx, "other", Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(ctx, "other", protocol.Agent{ID: "other", Name: "other", Kind: "human"}, "other-token"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.History(ctx, "other", room.ID, 0, 0, 10); !errors.Is(err, ErrForbidden) {
		t.Fatal("third reads history", err)
	}
	if _, err := s.SearchMessages(ctx, "other", room.ID, "private", 10); !errors.Is(err, ErrForbidden) {
		t.Fatal("third searches", err)
	}
	if contacts, _ := s.FederatedDMContacts(ctx, "other"); len(contacts) != 0 {
		t.Fatal("third sees contacts")
	}
	if _, err := s.OpenFederatedDM(ctx, "other", "conversation", 1); !errors.Is(err, ErrForbidden) {
		t.Fatal("third opens private DM", err)
	}
	// Even legacy/injected attachment rows must obey the current private scope.
	var message string
	s.db.QueryRow(`SELECT id FROM messages WHERE room_id=?`, room.ID).Scan(&message)
	f, _, err := s.RegisterFile(ctx, protocol.FileRef{ID: "private-file", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 1}, "aa/blob")
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`INSERT INTO attachments VALUES(?,?,?)`, message, f.Ref.ID, "private.bin")
	if ok, err := s.CanAccessFile(ctx, "other", f.Ref.ID); err != nil || ok {
		t.Fatal("third downloads private file", err)
	}
	token, err := s.RotateAgentToken(ctx, "local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if a, err := s.AgentByToken(ctx, token); err != nil || a.ID != "local" {
		t.Fatal("token rotation changed identity")
	}
	if _, _, err = s.History(ctx, "local", room.ID, 0, 0, 10); err != nil {
		t.Fatal(err)
	}
	if err = s.LeaveFederatedDM(ctx, "local", "conversation", 1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"local", "other"} {
		if _, _, err = s.History(ctx, id, room.ID, 0, 0, 10); !errors.Is(err, ErrForbidden) {
			t.Fatal("retired history access", err)
		}
		if got, err := s.Inbox(ctx, id, 20, []string{room.ID}, nil); err != nil || len(got) != 0 {
			t.Fatal("retired inbox leak", got, err)
		}
		if ok, _ := s.CanAccessFile(ctx, id, f.Ref.ID); ok {
			t.Fatal("retired attachment leak")
		}
	}
	if _, err = s.ImportFederationDM(ctx, "remote-node", e); !errors.Is(err, ErrForbidden) {
		t.Fatal("retired import allowed", err)
	}
	if err = s.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{dmConfig(1)}); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("retired epoch revived", err)
	}
	if err = s.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{dmConfig(2)}); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.OpenFederatedDM(ctx, "local", "conversation", 2)
	if err != nil || fresh.ID == room.ID {
		t.Fatal("new epoch reused history", fresh, err)
	}
	messages, _, _ := s.History(ctx, "local", fresh.ID, 0, 0, 10)
	if len(messages) != 0 {
		t.Fatal("old history exported")
	}
}

func TestFederatedDMDefaultOffAndRestartStableBinding(t *testing.T) {
	s, path, room := dmStore(t)
	ctx := context.Background()
	if _, err := s.ImportFederationDM(ctx, "remote-node", incomingDM(t, s)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopen, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopen.Close()
	if _, _, err = reopen.History(ctx, "local", room.ID, 0, 0, 10); !errors.Is(err, ErrForbidden) {
		t.Fatal("DM enabled by persisted state", err)
	}
	rooms, err := reopen.Rooms(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if r.ID == room.ID {
			t.Fatal("disabled DM listed")
		}
	}
	enableFederationTest(t, reopen, 100, 3)
	if err = reopen.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{dmConfig(1)}); err != nil {
		t.Fatal(err)
	}
	again, err := reopen.OpenFederatedDM(ctx, "local", "conversation", 1)
	if err != nil || again.ID != room.ID {
		t.Fatal(again, err)
	}
	if err = reopen.RevokeAgent(ctx, "local", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = reopen.History(ctx, "local", room.ID, 0, 0, 10); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked identity reads", err)
	}
	if _, err = reopen.ImportFederationDM(ctx, "remote-node", incomingDM(t, reopen)); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked recipient import", err)
	}
}

func TestFederatedDMThirdMembershipFailsClosedAndDeadRetryAudits(t *testing.T) {
	s, _, room := dmStore(t)
	ctx := context.Background()
	sent, err := s.SendMessage(ctx, "local", room.ID, protocol.SendMessageRequest{Text: "pending private"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimFederationDMJob(ctx, time.Now(), time.Minute)
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	var e FederationDMEnvelope
	json.Unmarshal([]byte(j.Payload), &e)
	if err = s.FinishFederationDMJob(ctx, *j, false, true, time.Now(), "peer rejected DM"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryFederationOutbox(ctx, j.ID, "retry-dm"); err != nil {
		t.Fatal(err)
	}
	if r, err := s.RetryFederationOutbox(ctx, j.ID, "retry-dm"); err != nil || !r.Replayed {
		t.Fatal(r, err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action='federation.outbox_retried'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	var shared string
	s.db.QueryRow(`SELECT bridge_agent_id FROM federation_node`).Scan(&shared)
	if _, err = s.db.Exec(`INSERT INTO memberships(room_id,agent_id,role,joined_at) VALUES(?,?,'member','test')`, room.ID, shared); err != nil {
		t.Fatal(err)
	}
	if s.FederationDMJobAuthorized(ctx, *j, e) {
		t.Fatal("third membership did not invalidate delivery")
	}
	if _, _, err = s.History(ctx, "local", room.ID, 0, 0, 10); !errors.Is(err, ErrForbidden) {
		t.Fatal("third membership not fail-closed", err)
	}
	messages, err := s.Inbox(ctx, "local", 20, []string{room.ID}, nil)
	if err != nil || len(messages) != 0 {
		t.Fatal(messages, err)
	}
	if err = s.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{dmConfig(1)}); !errors.Is(err, ErrForbidden) {
		t.Fatal("config silently repaired invalid membership", err)
	}
	if sent.Message.ID == "" {
		t.Fatal("fixture")
	}
}

func TestFederatedDMArchivedBindingAndChangedIdentitiesCannotReactivate(t *testing.T) {
	s, _, room := dmStore(t)
	ctx := context.Background()
	if err := s.ArchiveRoom(ctx, room.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportFederationDM(ctx, "remote-node", incomingDM(t, s)); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{dmConfig(1)}); !errors.Is(err, ErrForbidden) {
		t.Fatal("archived epoch silently reopened", err)
	}
	fresh := dmConfig(2)
	if err := s.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{fresh}); err != nil {
		t.Fatal(err)
	}
	changed := fresh
	changed.RemoteAgentID = "same-name-new-id"
	if err := s.ConfigureFederationDM(ctx, "remote-node", []FederationDMConversation{changed}); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("identity retargeted within epoch", err)
	}
}
