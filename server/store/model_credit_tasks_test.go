package store

import (
	"errors"
	"testing"
)

// dispatch 是 Relay opt-in 派发（X-CatsCo-Billing-Dispatch）用的第五个 action：收紧规则
// 和四个旧 action 一致，但它绝不带 legacy 路由偏好 —— 那是 start + model 才有的老契约，
// Relay 想优先走 legacy 额度时会继续调 start。
func TestModelCreditTaskActionDispatchIsAnActionOfItsOwn(t *testing.T) {
	for _, input := range []string{"dispatch", "DISPATCH", " dispatch ", "\tDispatch\n"} {
		normalized, ok := NormalizeModelCreditTaskAction(input)
		if !ok || normalized != ModelCreditTaskActionDispatch {
			t.Fatalf("%q normalized to %q ok=%v", input, normalized, ok)
		}
	}
	for _, old := range []string{
		ModelCreditTaskActionStart, ModelCreditTaskActionStatus,
		ModelCreditTaskActionResume, ModelCreditTaskActionFinish,
	} {
		if normalized, ok := NormalizeModelCreditTaskAction(old); !ok || normalized != old {
			t.Fatalf("old action %q normalized to %q ok=%v", old, normalized, ok)
		}
	}
	if _, ok := NormalizeModelCreditTaskAction("explode"); ok {
		t.Fatal("an unknown action was accepted")
	}

	dispatch := ModelCreditTaskInput{UID: 1, TaskID: "dispatch-root", Action: ModelCreditTaskActionDispatch}
	if err := dispatch.Validate(); err != nil {
		t.Fatalf("dispatch input rejected: %v", err)
	}
	// dispatch 不接受 legacy 偏好：老 Relay 的 legacy 路由仍然走 start。
	withPreference := dispatch
	withPreference.PreferLegacy = true
	if err := withPreference.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
		t.Fatalf("dispatch with a legacy preference: %v", err)
	}
	start := ModelCreditTaskInput{UID: 1, TaskID: "dispatch-root", Action: ModelCreditTaskActionStart, PreferLegacy: true}
	if err := start.Validate(); err != nil {
		t.Fatalf("start with a legacy preference rejected: %v", err)
	}
	// 门禁键的校验不放松：dispatch 也必须有合法的 uid 和 root id。
	badRoot := dispatch
	badRoot.TaskID = "not a token"
	if err := badRoot.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
		t.Fatalf("dispatch with a bad root id: %v", err)
	}
	badUID := dispatch
	badUID.UID = 0
	if err := badUID.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
		t.Fatalf("dispatch without a uid: %v", err)
	}
}
