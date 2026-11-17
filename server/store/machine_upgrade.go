package store

import "errors"

// 云机器点数升配的共享错误值。
//
// 这些 sentinel 放在 store 包里，是因为持久层（db/postgres）和 HTTP 层
// （server）都要用 errors.Is 判断同一个条件，而 server 不能反过来 import
// 具体适配器。
var (
	// ErrMachineUpgradeConflict：同一台机器已有未完成的升配，或同一个
	// client_request_id 被用在另一笔不同的升配上。
	ErrMachineUpgradeConflict = errors.New("machine upgrade conflicts with an existing operation")
	// ErrMachineUpgradeNotFound：这笔升配操作不存在或不属于该用户。
	ErrMachineUpgradeNotFound = errors.New("machine upgrade operation not found")
)
