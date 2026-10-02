// Package dialsched 实现单进程、串行调用的外呼名单内存调度器。
//
// 调度器在活动暂停、全局 DNC(拒接)标记与重试期限共同作用下,
// 从"活动 x 联系人"组合中挑选当前可拨的一项。
// 时间由调用方以整数秒传入,所有带时间的调用按时间单调不减执行。
package dialsched

// CampaignState 活动状态。
type CampaignState int

const (
	// Active 活动可参与取号。
	Active CampaignState = iota
	// Paused 活动暂停,仅影响后续领取,不撤销已开始的拨号。
	Paused
)

// Result 一次拨号的结局。
type Result int

const (
	// Resolved 拨号有定论,组合永久结束。
	Resolved Result = iota
	// NoAnswer 无人接听;累计不足 maxAttempts 次时按 retryDelay 重试,否则组合永久结束。
	NoAnswer
)

const (
	// retryDelay 是 NoAnswer 后距下次可拨的间隔秒数。
	retryDelay = 7200
	// maxAttempts 是每个组合的最大尝试次数。
	maxAttempts = 3
)

// Ticket 标识一次已开始的拨号,由 Take 返回、Finish 消费。
type Ticket uint64

// Membership 描述一条"联系人属于活动"的成员关系。
type Membership struct {
	CampaignID string
	ContactID  string
}

// Stats 报告调度器常驻状态量,用于验证空间上界。
type Stats struct {
	Combos            int // 组合(成员关系)总数,即 N
	CampaignHeapItems int // 各活动堆中驻留的候选总数
	GlobalHeapItems   int // 全局堆中的活动数(<= 活动数)
	InFlight          int // 未完成的拨号数(<= 联系人数)
}
