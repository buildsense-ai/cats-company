package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// bot-as-tool：把一次 run 交给另一个 bot 执行。测试覆盖
// 「解析 / 提示词提取 / 离线快速失败 / 正常收尾 / 超时 / 白名单」。

type fakeBotStore struct {
	mu sync.Mutex

	owner      int64
	ownerErr   error
	started    bool
	startErr   error
	startedRun []string
	completed  []string
	completedR json.RawMessage
	completedE ServiceRunEnvelope
	// completedByRun 按 run_id 留一份 envelope，供「并发时各拿各的」类断言使用。
	completedByRun map[string]ServiceRunEnvelope
	failed         map[string]string
	startCalls     int

	saved    []fakeBotMessage
	saveErr  error
	nextID   int64
	messages []*types.Message
	readErr  error
	// topics 记录 CreateTopic 调用：投递前必须先建 topic（messages 有外键）。
	topics   []string
	topicErr error
	// leases 记录每次 StartServiceRun 的租约秒数（由本次超时推导）。
	leases []int
}

type fakeBotMessage struct {
	topic    string
	from     int64
	text     string
	typ      string
	session  string
	metadata map[string]interface{}
}

func newFakeBotStore() *fakeBotStore {
	return &fakeBotStore{
		owner:          38,
		started:        true,
		nextID:         100,
		failed:         map[string]string{},
		completedByRun: map[string]ServiceRunEnvelope{},
	}
}

func (f *fakeBotStore) StartServiceRun(_ context.Context, runID, instanceID string, leaseSeconds int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	f.startedRun = append(f.startedRun, instanceID)
	// 租约由本次 run 的超时推导（超时 + 60s），所以它同时是「这次用了哪套
	// 执行参数」的观测口 —— 热更新测试靠它证明配置真的换了。
	f.leases = append(f.leases, leaseSeconds)
	return f.started, f.startErr
}

// lastLease 返回最后一次 StartServiceRun 收到的租约秒数。
func (f *fakeBotStore) lastLease() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.leases) == 0 {
		return 0
	}
	return f.leases[len(f.leases)-1]
}

// leaseFor 返回第 i 次 StartServiceRun 的租约秒数（-1 = 最后一次）。
func (f *fakeBotStore) leaseFor(i int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.leases) {
		return 0
	}
	return f.leases[i]
}

func (f *fakeBotStore) CompleteServiceRun(_ context.Context, runID, resultID string, result, usage json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, runID)
	f.completedR = result
	_ = json.Unmarshal(result, &f.completedE)
	if f.completedByRun == nil {
		f.completedByRun = map[string]ServiceRunEnvelope{}
	}
	// 必须深拷贝：json.RawMessage 反序列化会复用底层数组，
	// 否则后一个 run 的结果会把前一个 run 记下来的 envelope 一起改掉。
	snapshot := f.completedE
	if f.completedE.Output != nil {
		snapshot.Output = append(json.RawMessage(nil), f.completedE.Output...)
	}
	f.completedByRun[runID] = snapshot
	return true, nil
}

func (f *fakeBotStore) FailServiceRun(_ context.Context, runID, errorText string, retryable bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[runID] = errorText
	return nil
}

// —— 线程安全的读方法 ——
//
// 写入都在 mu 内（runner 跑在后台 goroutine），测试读不加锁就是数据竞态：
// 实测并发跑时读到零值（status 空），单跑却正常 —— 正是缺 happens-before。

func (f *fakeBotStore) completedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.completed)
}

func (f *fakeBotStore) failedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.failed)
}

func (f *fakeBotStore) errorText(runID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failed[runID]
}

// result 返回某个 run 的结果 envelope（深拷贝，避免与后台写共享底层数组）。
func (f *fakeBotStore) result(t *testing.T, runID string) ServiceRunEnvelope {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	env, ok := f.completedByRun[runID]
	if !ok {
		t.Fatalf("run %s 还没有结果", runID)
	}
	return env
}

// lastResult 返回最后完成的那次结果（单 run 测试用）。
func (f *fakeBotStore) lastResult(t *testing.T) ServiceRunEnvelope {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.completed) == 0 {
		t.Fatal("还没有完成的 run")
	}
	env, ok := f.completedByRun[f.completed[len(f.completed)-1]]
	if !ok {
		t.Fatal("找不到最后那个 run 的结果")
	}
	return env
}

func (f *fakeBotStore) SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error) {
	id, _, err := f.SaveMessageWithMetadata(topicID, fromUID, content, nil, "", "", msgType, 0, "", nil)
	return id, err
}

// CreateTopic 记录调用方要求建的 topic：生产上 messages.topic_id 有外键，
// 不建就是 foreign key 报错（跨账号首调必炸）。
func (f *fakeBotStore) CreateTopic(id, topicType string, ownerID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.topicErr != nil {
		return f.topicErr
	}
	f.topics = append(f.topics, id)
	return nil
}

func (f *fakeBotStore) SaveMessageWithMetadata(topicID string, fromUID int64, content string, _ []types.ContentBlock,
	_, _, msgType string, _ int64, _ string, metadata map[string]interface{}) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return 0, false, f.saveErr
	}
	f.nextID++
	id := f.nextID
	record := fakeBotMessage{topic: topicID, from: fromUID, text: content, typ: msgType, metadata: metadata}
	if raw, ok := metadata[botSessionMetadataKey].(string); ok {
		record.session = raw
	}
	f.saved = append(f.saved, record)
	return id, true, nil
}

func (f *fakeBotStore) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	var out []*types.Message
	for _, msg := range f.messages {
		if msg.TopicID == topicID && msg.ID > sinceID {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (f *fakeBotStore) GetBotOwner(botUID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ownerErr != nil {
		return 0, f.ownerErr
	}
	return f.owner, nil
}

func (f *fakeBotStore) pushBotReply(topic string, botUID int64, text string) {
	f.pushBotReplyFor(topic, botUID, text, 0, "")
}

// pushBotReplyFor 推一条「带归属信息」的回复：新客户端会回填 reply_to（或回传会话 id）。
func (f *fakeBotStore) pushBotReplyFor(topic string, botUID int64, text string, replyTo int64, sessionID string) {
	f.pushBotMessage(topic, botUID, text, replyTo, sessionID, "")
}

// pushBotMessage 推一条 bot 消息，可指定 mode。
// `mode=code` 是 bot 的工具调用/输出展示（skill 文件内容、shell stdout），
// 不是给调用方的答案。
func (f *fakeBotStore) pushBotMessage(topic string, botUID int64, text string, replyTo int64, sessionID, mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	msg := &types.Message{
		ID: f.nextID, TopicID: topic, FromUID: botUID, Content: text, MsgType: "text", ReplyTo: replyTo, Mode: mode,
	}
	if sessionID != "" {
		msg.Metadata = map[string]interface{}{botSessionMetadataKey: sessionID}
	}
	f.messages = append(f.messages, msg)
}

type fakeBotHub struct {
	online     bool
	delivered  []string
	sessions   []string
	deliverErr bool
}

func (f *fakeBotHub) DeliverServiceBotPrompt(callerUID, botUID int64, topicID, text string, msgID int64, botSessionID string) bool {
	f.delivered = append(f.delivered, topicID+"|"+text)
	f.sessions = append(f.sessions, botSessionID)
	return !f.deliverErr
}

func (f *fakeBotHub) BotRuntimeOnline(botUID int64) bool { return f.online }

// newTestBotRunner 用短超时/短轮询构造，避免测试真的等 5 分钟。
func newTestBotRunner(db botRunnerStore, hub botRunnerHub) *ServiceBotRunner {
	runner := &ServiceBotRunner{
		store:          db,
		hub:            hub,
		timeout:        300 * time.Millisecond,
		poll:           5 * time.Millisecond,
		settle:         10 * time.Millisecond,
		ambiguityGrace: 60 * time.Millisecond,
		inFlight:       map[string]int{},
	}
	return runner
}

func botAgent(t *testing.T, definition string) store.ServiceAgentDescriptor {
	t.Helper()
	return store.ServiceAgentDescriptor{
		ID: 1, Slug: "jack-service", Name: "杰克服务",
		Description: "把活派给杰克", DefinitionJSON: definition,
	}
}

func waitFor(t *testing.T, cond func() bool, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestParseBotRunnerBotUID(t *testing.T) {
	cases := []struct {
		name       string
		definition string
		wantUID    int64
		wantOK     bool
	}{
		{"bot runner", `{"runner":{"type":"bot","bot_uid":982}}`, 982, true},
		{"大小写与空格", `{"runner":{"type":" BOT ","bot_uid":7}}`, 7, true},
		{"缺 bot_uid", `{"runner":{"type":"bot"}}`, 0, false},
		{"bot_uid 为 0", `{"runner":{"type":"bot","bot_uid":0}}`, 0, false},
		{"worker 不是 bot", `{"runner":{"type":"worker","command":"x"}}`, 0, false},
		{"没有 runner", `{"description":"x"}`, 0, false},
		{"坏 json", `{`, 0, false},
		{"空 definition", ``, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid, ok := ParseBotRunnerBotUID(json.RawMessage(tc.definition))
			if uid != tc.wantUID || ok != tc.wantOK {
				t.Fatalf("got (%d,%v), want (%d,%v)", uid, ok, tc.wantUID, tc.wantOK)
			}
		})
	}
}

func TestBotRunnerPromptText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"text 字段（最常见）", `{"text":"帮我润色这句话"}`, "帮我润色这句话"},
		{"纯字符串", `"直接把字符串当话"`, "直接把字符串当话"},
		{"空对象没有内容", `{}`, ""},
		{"null", `null`, ""},
		{"空串", ``, ""},
		{"只有空白的 text", `{"text":"   "}`, ""},
		{"其他结构整体下发", `{"query":"x","n":2}`, `{"query":"x","n":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := botRunnerPromptText(json.RawMessage(tc.input)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBotRunnerTopicIsOrdered(t *testing.T) {
	if got := botRunnerTopic(110, 38); got != "p2p_38_110" {
		t.Fatalf("got %q, want p2p_38_110", got)
	}
	if got := botRunnerTopic(38, 110); got != "p2p_38_110" {
		t.Fatalf("got %q, want p2p_38_110", got)
	}
}

// bot 不在线时必须立刻失败，而不是等到超时 —— 用户等 5 分钟才发现没人接最糟。
func TestBotRunnerOfflineFailsFast(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: false}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_1", SessionID: "sess_1", Input: json.RawMessage(`{"text":"hi"}`)}, 38)

	if !waitFor(t, func() bool { return db.failedCount() > 0 }, 2*time.Second) {
		t.Fatal("run 应该被判定失败")
	}
	if !strings.HasPrefix(db.errorText("run_1"), "bot_offline") {
		t.Fatalf("errorText = %q, want bot_offline 前缀", db.errorText("run_1"))
	}
	if len(db.saved) != 0 {
		t.Fatal("离线时不该把消息落库")
	}
}

// 正常路径：投递 → 收到 bot 回复 → 写 run 结果。
func TestBotRunnerCompletesWithBotReply(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
			store.ServiceRun{RunID: "run_2", SessionID: "sess_2", Input: json.RawMessage(`{"text":"润色一下"}`)}, 38)
	}()

	// 等消息投递出去后，模拟 bot 回话。
	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("应该已经投递")
	}
	db.pushBotReply("p2p_38_982", 982, "润色好了：请大家及时修改密码。")

	if !waitFor(t, func() bool { return db.completedCount() > 0 }, 3*time.Second) {
		t.Fatalf("run 未完成；failed=%v", db.failed)
	}
	env := db.lastResult(t)
	if env.Status != ServiceRunCompleted {
		t.Fatalf("status = %v, want completed", env.Status)
	}
	var output struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(env.Output, &output); err != nil {
		t.Fatalf("output 解析失败: %v", err)
	}
	if !strings.Contains(output.Text, "请大家及时修改密码") {
		t.Fatalf("output.text = %q", output.Text)
	}
	if len(db.saved) != 1 || db.saved[0].from != 38 || db.saved[0].text != "润色一下" {
		t.Fatalf("落库的消息不对: %+v", db.saved)
	}
	<-done
}

// 只认 bot 自己发的 text：工具卡片 / 别人的消息都不能当结果。
func TestBotRunnerIgnoresNonBotAndNonText(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_3", SessionID: "sess_3", Input: json.RawMessage(`{"text":"hi"}`)}, 38)

	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("应该已经投递")
	}
	db.mu.Lock()
	db.nextID++
	db.messages = append(db.messages, &types.Message{
		ID: db.nextID, TopicID: "p2p_38_982", FromUID: 38, Content: "我自己发的", MsgType: "text",
	})
	db.nextID++
	db.messages = append(db.messages, &types.Message{
		ID: db.nextID, TopicID: "p2p_38_982", FromUID: 982, Content: "tool_use", MsgType: "tool_use",
	})
	db.mu.Unlock()

	// 这些都不算回复 → 应超时失败。
	if !waitFor(t, func() bool { return db.failedCount() > 0 }, 3*time.Second) {
		t.Fatal("应该超时失败")
	}
	if !strings.HasPrefix(db.errorText("run_3"), "bot_timeout") {
		t.Fatalf("errorText = %q, want bot_timeout", db.errorText("run_3"))
	}
	if db.completedCount() != 0 {
		t.Fatal("不该有完成结果")
	}
}

// bot 回多段（会被前端按长度切段）时要拼起来，不能只留第一段。
func TestBotRunnerJoinsMultiPartReply(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)
	runner.settle = 120 * time.Millisecond // 留出收多段的窗口

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_4", SessionID: "sess_4", Input: json.RawMessage(`{"text":"hi"}`)}, 38)

	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("应该已经投递")
	}
	db.pushBotReply("p2p_38_982", 982, "第一段")
	db.pushBotReply("p2p_38_982", 982, "第二段")

	if !waitFor(t, func() bool { return db.completedCount() > 0 }, 3*time.Second) {
		t.Fatalf("run 未完成；failed=%v", db.failed)
	}
	var output struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(db.lastResult(t).Output, &output)
	if output.Text != "第一段\n\n第二段" {
		t.Fatalf("output.text = %q, want 两段拼接", output.Text)
	}
}

// 投递失败（bot 连接不在这台机器上）必须如实失败，不能让 run 卡到超时。
func TestBotRunnerDeliverFailureFailsRun(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true, deliverErr: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_5", SessionID: "sess_5", Input: json.RawMessage(`{"text":"hi"}`)}, 38)

	if !waitFor(t, func() bool { return db.failedCount() > 0 }, 2*time.Second) {
		t.Fatal("应该失败")
	}
	if !strings.HasPrefix(db.errorText("run_5"), "bot_deliver_failed") {
		t.Fatalf("errorText = %q", db.errorText("run_5"))
	}
}

// 白名单：非白名单账号名下的 bot 不能被别人当工具调用。
func TestBotRunnerOwnerWhitelist(t *testing.T) {
	db := newFakeBotStore()
	db.owner = 999 // bot 属于 999，不在白名单
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)
	runner.SetOwnerWhitelist([]int64{38, 2})

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_6", SessionID: "sess_6", Input: json.RawMessage(`{"text":"hi"}`)}, 38)

	if !waitFor(t, func() bool { return db.failedCount() > 0 }, 2*time.Second) {
		t.Fatal("应该拒绝")
	}
	if !strings.HasPrefix(db.errorText("run_6"), "bot_not_shared") {
		t.Fatalf("errorText = %q, want bot_not_shared", db.errorText("run_6"))
	}
}

// 自己的 bot 不受白名单限制（自己的东西自己用）。
func TestBotRunnerOwnBotAlwaysAllowed(t *testing.T) {
	db := newFakeBotStore()
	db.owner = 999
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)
	runner.SetOwnerWhitelist([]int64{38})
	runner.timeout = 100 * time.Millisecond

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_7", SessionID: "sess_7", Input: json.RawMessage(`{"text":"hi"}`)}, 999)

	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatalf("自己的 bot 应该允许投递；failed=%v", db.failed)
	}
}

// run 已被别的执行方占住（幂等重放 / 并发）时不能再投递一次。
func TestBotRunnerSkipsAlreadyStartedRun(t *testing.T) {
	db := newFakeBotStore()
	db.started = false // StartServiceRun 返回 false = 抢不到
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_8", SessionID: "sess_8", Input: json.RawMessage(`{"text":"hi"}`)}, 38)

	// 给 goroutine 一点时间，然后确认什么都没发。
	time.Sleep(100 * time.Millisecond)
	if len(db.saved) != 0 || len(hub.delivered) != 0 {
		t.Fatal("抢不到 run 时不该投递")
	}
}

// 非 bot runner 必须完全不介入（这是加这个功能最需要保证的兼容性）。
func TestBotRunnerIgnoresOtherRunners(t *testing.T) {
	for _, definition := range []string{
		`{"runner":{"type":"worker","command":"node x.js"}}`,
		`{"runner":{"type":"http","url":"https://example.test/run"}}`,
		`{"description":"没有 runner"}`,
	} {
		db := newFakeBotStore()
		hub := &fakeBotHub{online: true}
		runner := newTestBotRunner(db, hub)
		runner.MaybeDispatch(context.Background(), botAgent(t, definition),
			store.ServiceRun{RunID: "run_x", SessionID: "sess_x", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
		time.Sleep(50 * time.Millisecond)
		if db.startCalls != 0 || len(hub.delivered) != 0 {
			t.Fatalf("%s 不该被 bot runner 接管", definition)
		}
	}
}
