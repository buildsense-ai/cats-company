package server

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// bot-as-tool 的执行参数**可云端调整**，不需要发客户端、不需要重启平台。
//
// 优先级（后者覆盖前者）：
//
//	内置默认  →  平台环境变量  →  agent definition 的 runner 字段
//
// 最高优先级的 `definition.runner` 是**每次调用重新读**的（见 MaybeDispatch），
// 所以改一个已上架应用的 runner 参数，下一次调用立刻生效 —— 这就是「实时更新」
// 的实现方式：改的是数据，不是代码。
//
// 之所以把这三档分开而不是全部塞进 env：env 改完要重启进程，适合「平台级默认」；
// 单个应用想单独调（比如某个 bot 特别慢，要给 15 分钟）就得走 definition，
// 那是个应用自己的属性，跟别人无关 —— 控制单一变量。
const (
	// 平台级默认（env，改完需重启；适合调全站基调）。
	envBotRunnerTimeoutSeconds        = "CATSCO_BOT_RUNNER_TIMEOUT_SECONDS"
	envBotRunnerSettleSeconds         = "CATSCO_BOT_RUNNER_SETTLE_SECONDS"
	envBotRunnerAmbiguityGraceSeconds = "CATSCO_BOT_RUNNER_AMBIGUITY_GRACE_SECONDS"
	envBotRunnerPollMillis            = "CATSCO_BOT_RUNNER_POLL_INTERVAL_MS"

	// 单应用覆盖（definition.runner，改完下一次调用即生效；不需要重启）。
	fieldBotRunnerTimeoutSeconds        = "timeout_seconds"
	fieldBotRunnerSettleSeconds         = "settle_seconds"
	fieldBotRunnerAmbiguityGraceSeconds = "ambiguity_grace_seconds"
	fieldBotRunnerPollMillis            = "poll_interval_ms"
)

// 允许区间：防止一个手滑的 0 或 86400 把平台变成「永远不超时」或「立刻超时」。
const (
	minBotRunnerTimeout = 10 * time.Second
	maxBotRunnerTimeout = 30 * time.Minute
	// settle 下限不能是 0：settle=0 会让「第一段到手就收尾」，而 bot 会把长回复
	// 按 4000 字切段 —— 于是答案被默默截断，调用方拿到半句话且完全看不出异常。
	minBotRunnerSettle         = 500 * time.Millisecond
	maxBotRunnerSettle         = 30 * time.Second
	minBotRunnerAmbiguityGrace = 1 * time.Second
	maxBotRunnerAmbiguityGrace = 5 * time.Minute
	minBotRunnerPoll           = 50 * time.Millisecond
	maxBotRunnerPoll           = 5 * time.Second

	// botDefinitionReadTimeout 是「热更新多读一次 definition」的上限。
	// 读不到就用会话快照派活 —— 它只影响 bot 多快开始干活，不影响调用方拿到句柄。
	botDefinitionReadTimeout = 2 * time.Second
)

// botRunnerTuning 是一次 run 实际使用的执行参数（已合并三层、已夹到允许区间）。
type botRunnerTuning struct {
	// Timeout 是整单的等待预算：bot 在这个时间内没回完就按超时收尾。
	Timeout time.Duration
	// Settle 是「收到一段后等多久没新内容就收尾」的静默窗口。
	// bot 会把长回复按 4000 字切段，收太早会截断答案。
	Settle time.Duration
	// AmbiguityGrace 是「看得见回复却判断不出是不是给我」时等多久才如实失败。
	AmbiguityGrace time.Duration
	// Poll 是轮询消息表的间隔。
	Poll time.Duration
}

// builtinBotRunnerTuning 是没有任何配置时的行为（等于历史实现）。
func builtinBotRunnerTuning() botRunnerTuning {
	return botRunnerTuning{
		Timeout:        defaultBotRunnerTimeout,
		Settle:         defaultBotRunnerSettle,
		AmbiguityGrace: botReplyAmbiguousGrace,
		Poll:           defaultBotRunnerPollInterval,
	}
}

// platformBotRunnerTuning 是「内置默认 + 平台 env」。
func platformBotRunnerTuning() botRunnerTuning {
	t := builtinBotRunnerTuning()
	if v, ok := botRunnerEnvSeconds(envBotRunnerTimeoutSeconds); ok {
		t.Timeout = v
	}
	if v, ok := botRunnerEnvSeconds(envBotRunnerSettleSeconds); ok {
		t.Settle = v
	}
	if v, ok := botRunnerEnvSeconds(envBotRunnerAmbiguityGraceSeconds); ok {
		t.AmbiguityGrace = v
	}
	if v, ok := botRunnerEnvMillis(envBotRunnerPollMillis); ok {
		t.Poll = v
	}
	return t.clamped()
}

// runnerTuningOverrides 是 definition.runner 里的调参字段。
// 用指针区分「没写」（nil）和「写了个 0」—— 前者保持上层默认，后者要生效。
type runnerTuningOverrides struct {
	TimeoutSeconds        *float64 `json:"timeout_seconds"`
	SettleSeconds         *float64 `json:"settle_seconds"`
	AmbiguityGraceSeconds *float64 `json:"ambiguity_grace_seconds"`
	PollIntervalMillis    *float64 `json:"poll_interval_ms"`
}

// botRunnerTuningFor 解析一次调用要用的参数：平台默认 + definition.runner 覆盖。
//
// definition 传空（老应用、解析失败）时就是平台默认 —— 不因为读不到配置就改变行为。
func botRunnerTuningFor(definition json.RawMessage) botRunnerTuning {
	t := platformBotRunnerTuning()
	if len(definition) == 0 {
		return t
	}
	var parsed struct {
		Runner *runnerTuningOverrides `json:"runner"`
	}
	if err := json.Unmarshal(definition, &parsed); err != nil || parsed.Runner == nil {
		return t
	}
	o := parsed.Runner
	if o.TimeoutSeconds != nil {
		t.Timeout = secondsToDuration(*o.TimeoutSeconds)
	}
	if o.SettleSeconds != nil {
		t.Settle = secondsToDuration(*o.SettleSeconds)
	}
	if o.AmbiguityGraceSeconds != nil {
		t.AmbiguityGrace = secondsToDuration(*o.AmbiguityGraceSeconds)
	}
	if o.PollIntervalMillis != nil {
		t.Poll = millisToDuration(*o.PollIntervalMillis)
	}
	return t.clamped()
}

// clamped 把四个值夹到允许区间，并保证「静默窗口/歧义窗口」不会长过整单预算
// —— 否则它们永远不会触发，等于配置没写。
func (t botRunnerTuning) clamped() botRunnerTuning {
	t.Timeout = clampDuration(t.Timeout, minBotRunnerTimeout, maxBotRunnerTimeout)
	// 窗口不能长过整单预算，否则永远不会触发（等于配置没写）。
	t.Settle = clampDuration(t.Settle, min(minBotRunnerSettle, t.Timeout), min(maxBotRunnerSettle, t.Timeout))
	t.AmbiguityGrace = clampDuration(t.AmbiguityGrace,
		min(minBotRunnerAmbiguityGrace, t.Timeout),
		min(maxBotRunnerAmbiguityGrace, t.Timeout))
	t.Poll = clampDuration(t.Poll, minBotRunnerPoll, min(maxBotRunnerPoll, t.Timeout))
	return t
}

func clampDuration(v, lo, hi time.Duration) time.Duration {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// secondsToDuration 把「秒」转成 Duration，顺手挡掉 NaN / Inf / 负数
// （JSON 里 1e400 能进来，直接乘 time.Second 会溢出成一个荒谬的负数）。
func secondsToDuration(seconds float64) time.Duration {
	if seconds != seconds || seconds < 0 || seconds > 1e6 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func millisToDuration(millis float64) time.Duration {
	if millis != millis || millis < 0 || millis > 1e9 {
		return 0
	}
	return time.Duration(millis * float64(time.Millisecond))
}

// botRunnerEnvSeconds 读一个「秒」的 env。
//
// 设了但用不了（拼错、负数、非数字）时**打一行日志**：静默忽略是最坏的 ——
// 运维以为调了参数，实际跑的还是默认值，而界面上一切正常。
func botRunnerEnvSeconds(name string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("bot runner: ignore %s=%q (not a number)", name, raw)
		return 0, false
	}
	d := secondsToDuration(value)
	if d <= 0 {
		log.Printf("bot runner: ignore %s=%q (out of range)", name, raw)
		return 0, false
	}
	return d, true
}

func botRunnerEnvMillis(name string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("bot runner: ignore %s=%q (not a number)", name, raw)
		return 0, false
	}
	d := millisToDuration(value)
	if d <= 0 {
		log.Printf("bot runner: ignore %s=%q (out of range)", name, raw)
		return 0, false
	}
	return d, true
}
