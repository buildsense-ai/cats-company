package server

import "sync"

// topicArrivalNotifier 是进程内的「某个 topic 有新消息落库」广播。
//
// 为什么需要它：bot runner 判断「bot 回没回」原来是每 500ms 查一次消息表
// （GetMessagesSince）。一次几小时的长任务就是四万多次查询，其中绝大多数查到
// 的是空 —— 而真正要等的那条消息落库的瞬间就在同一个进程里，只是没人告诉
// 等待方。
//
// 所以改成「落库即通知」：写消息的路径调 NotifyMessageArrived，等回复的 run
// 订阅自己那个 topic。查库退化成安全网 —— 多实例部署、别的进程直接写库、
// 或者哪天新加了一条落库路径忘了通知，回复也不会烂在原地。
type topicArrivalNotifier struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newTopicArrivalNotifier() *topicArrivalNotifier {
	return &topicArrivalNotifier{waiters: map[string]map[chan struct{}]struct{}{}}
}

// Subscribe 订阅某个 topic 的「有新消息」事件，返回事件 channel 与退订函数。
//
// channel 带 1 个缓冲，而且通知本身不含消息内容（只有「去查一次」的意思）：
// 同一轮落库好几条时，挤掉重复通知不丢任何信息，本来也是查一次就能一起读到。
//
// 退订函数可重复调用；某个 topic 上最后一个订阅者退出时，键会被删掉，长跑进程
// 不会攒出一张越来越大的表。
func (n *topicArrivalNotifier) Subscribe(topic string) (<-chan struct{}, func()) {
	if n == nil || topic == "" {
		return nil, func() {}
	}
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.waiters == nil {
		n.waiters = map[string]map[chan struct{}]struct{}{}
	}
	if n.waiters[topic] == nil {
		n.waiters[topic] = map[chan struct{}]struct{}{}
	}
	n.waiters[topic][ch] = struct{}{}
	n.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			n.mu.Lock()
			if set, ok := n.waiters[topic]; ok {
				delete(set, ch)
				if len(set) == 0 {
					delete(n.waiters, topic)
				}
			}
			n.mu.Unlock()
		})
	}
	return ch, cancel
}

// Notify 唤醒该 topic 上的所有等待方。不阻塞：没人等就是空操作。
func (n *topicArrivalNotifier) Notify(topic string) {
	if n == nil || topic == "" {
		return
	}
	n.mu.Lock()
	set := n.waiters[topic]
	chans := make([]chan struct{}, 0, len(set))
	for ch := range set {
		chans = append(chans, ch)
	}
	n.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default:
			// 已经有一条未消费的通知：这条挤掉即可，语义上完全等价。
		}
	}
}

// waiterCount 只给测试用：断言退订之后表里真的没有残留。
func (n *topicArrivalNotifier) waiterCount(topic string) int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.waiters[topic])
}

// defaultTopicArrivals 是进程级默认总线。生产上唯一的写入者就是消息落库路径，
// 所以不需要给每个 handler 注入一份 —— 那样只会多几条可能忘接的线。
var defaultTopicArrivals = newTopicArrivalNotifier()

// NotifyMessageArrived 在消息**确实新增**落库之后调用，唤醒同一 topic 上等
// 回复的 bot runner。
//
// 重复消息（幂等重发命中 client_msg_id）不要调：没有产生新内容，唤醒只会换来
// 一次空查询。
func NotifyMessageArrived(topic string) { defaultTopicArrivals.Notify(topic) }

// SubscribeMessageArrivals 订阅某个 topic 的新消息到达事件。
func SubscribeMessageArrivals(topic string) (<-chan struct{}, func()) {
	return defaultTopicArrivals.Subscribe(topic)
}
