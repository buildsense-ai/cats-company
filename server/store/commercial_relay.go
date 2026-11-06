package store

import "errors"

// ErrCommercialRelayUserMissing 表示 relay 侧的账号在平台 users 表里不存在。
//
// 两种来源：账号已删除但 relay key 仍 active；或者 key 用的是平台外的合成 uid
// （例如平台自己的服务 key cats-company-server / 9900020699）。
//
// 建商业基线的第一步就是拿 users 行的行锁（SELECT ... FROM users FOR UPDATE），
// 所以这个错误重试多少次都不会成功：调用方必须把它当终态，而不是当瞬时故障。
var ErrCommercialRelayUserMissing = errors.New("commercial relay baseline user not found")
