// Package callsched 实现单进程、串行调用的外呼名单调度库。
//
// 调度规则：每个（活动, 联系人）组合独立维护可拨时刻与尝试次数；
// 同一联系人跨所有活动最多一次未完成拨号；Take 总是返回满足
// （活动Active、联系人无DNC且未占用、组合未结束、可拨时刻<=now）
// 的全部组合中按（可拨时刻, 活动ID, 联系人ID）升序的首项。
//
// 复杂度（N=成员关系数, k=单联系人成员关系数, k<=10, 活动数<=10）：
//   - SetCampaignState: O(1)，只翻转标志位，不触碰成员；
//   - SetDoNotCall / Take / Finish: O(k log N)，
//     只在状态翻转的联系人的 k 个组合上做堆插入/删除；
//   - 常驻状态 O(N+U)：每个组合至多在一个堆中，票据数不超过占用中的联系人数。
package callsched

// CampaignState 表示活动状态。
type CampaignState int

const (
	Active CampaignState = iota
	Paused
)

// Result 表示一次拨号的结局。
type Result int

const (
	Resolved Result = iota
	NoAnswer
)

// retryInterval 是 NoAnswer 后的重试间隔（秒）。
const retryInterval int64 = 7200

// maxAttempts 是每个组合的尝试预算上限。
const maxAttempts = 3

// Membership 描述初始化时的一条活动-联系人成员关系。
type Membership struct {
	CampaignID string
	ContactID  string
}

// Ticket 是 Take 发放的拨号票据，用于 Finish 核销。
type Ticket uint64

// Call 是 Take 成功时返回的领取结果。
type Call struct {
	CampaignID string
	ContactID  string
	Ticket     Ticket
}

type combo struct {
	campaign   *campaign
	contactID  string
	dialableAt int64
	attempts   int
	ended      bool
	heapIdx    int
}

type campaign struct {
	id     string
	active bool
	h      comboHeap
}

type contact struct {
	id       string
	dnc      bool
	occupied bool
	combos   []*combo
}

// Scheduler 是外呼名单调度器，非并发安全，仅供单进程串行使用。
type Scheduler struct {
	campaigns  map[string]*campaign
	contacts   map[string]*contact
	tickets    map[Ticket]*combo
	nextTicket Ticket
}

// New 以固定的活动、联系人及成员关系初始化调度器。
// 活动初始均为 Active；每个组合初始可拨时刻为 0、尝试次数为 0。
func New(memberships []Membership) *Scheduler {
	s := &Scheduler{
		campaigns: make(map[string]*campaign),
		contacts:  make(map[string]*contact),
		tickets:   make(map[Ticket]*combo),
	}
	for _, m := range memberships {
		cp, ok := s.campaigns[m.CampaignID]
		if !ok {
			cp = &campaign{id: m.CampaignID, active: true}
			s.campaigns[m.CampaignID] = cp
		}
		ct, ok := s.contacts[m.ContactID]
		if !ok {
			ct = &contact{id: m.ContactID}
			s.contacts[m.ContactID] = ct
		}
		c := &combo{campaign: cp, contactID: m.ContactID, heapIdx: -1}
		ct.combos = append(ct.combos, c)
		cp.h.push(c)
	}
	return s
}

// SetCampaignState 在 Active 与 Paused 之间切换活动状态。
// O(1)：暂停/恢复不遍历成员，堆保持不动，Take 时按标志位跳过整个活动的堆。
func (s *Scheduler) SetCampaignState(id string, state CampaignState) {
	cp, ok := s.campaigns[id]
	if !ok {
		return
	}
	cp.active = state == Active
}

// SetDoNotCall 设置或解除联系人的全局 DNC标记。
// O(k log N)：只在该联系人的 k 个组合上做堆删除/恢复，
// 不修改可拨时刻与尝试次数，解除后原重试状态原样恢复。
func (s *Scheduler) SetDoNotCall(contactID string, blocked bool) {
	ct, ok := s.contacts[contactID]
	if !ok || ct.dnc == blocked {
		return
	}
	ct.dnc = blocked
	if blocked {
		for _, c := range ct.combos {
			if c.heapIdx >= 0 {
				c.campaign.h.remove(c)
			}
		}
		return
	}
	if ct.occupied {
		return
	}
	for _, c := range ct.combos {
		if !c.ended && c.heapIdx < 0 {
			c.campaign.h.push(c)
		}
	}
}

// lessCombo 是跨活动的全序：(可拨时刻, 活动ID, 联系人ID) 字典序。
func lessCombo(a, b *combo) bool {
	if a.dialableAt != b.dialableAt {
		return a.dialableAt < b.dialableAt
	}
	if a.campaign.id != b.campaign.id {
		return a.campaign.id < b.campaign.id
	}
	return a.contactID < b.contactID
}

// Take 领取一个当前可拨的组合；没有候选时返回 ok=false。
// 成功领取立即占用联系人并消耗一次尝试。
// O(k log N)：活动数<=10，只比较各 Active 活动堆顶；
// 被暂停、DNC、占用的组合不在任何 Active 堆的可选范围内，无需逐项跳过。
func (s *Scheduler) Take(now int64) (Call, bool) {
	var best *combo
	for _, cp := range s.campaigns {
		if !cp.active {
			continue
		}
		top := cp.h.top()
		if top == nil || top.dialableAt > now {
			continue
		}
		if best == nil || lessCombo(top, best) {
			best = top
		}
	}
	if best == nil {
		return Call{}, false
	}
	best.campaign.h.remove(best)
	best.attempts++
	ct := s.contacts[best.contactID]
	ct.occupied = true
	for _, c := range ct.combos {
		if c.heapIdx >= 0 {
			c.campaign.h.remove(c)
		}
	}
	t := s.nextTicket
	s.nextTicket++
	s.tickets[t] = best
	return Call{CampaignID: best.campaign.id, ContactID: best.contactID, Ticket: t}, true
}

// Finish 结束一次拨号并释放联系人。
// Resolved 永久结束该组合；NoAnswer 且累计尝试不足 3 次时，
// 下次可拨时刻设为 now+7200，否则永久结束。
// O(k log N)：释放联系人后，其全部未结束组合立即重新入堆参与选择。
func (s *Scheduler) Finish(ticket Ticket, result Result, now int64) {
	c, ok := s.tickets[ticket]
	if !ok {
		return
	}
	delete(s.tickets, ticket)
	ct := s.contacts[c.contactID]
	ct.occupied = false
	switch result {
	case Resolved:
		c.ended = true
	case NoAnswer:
		if c.attempts < maxAttempts {
			c.dialableAt = now + retryInterval
		} else {
			c.ended = true
		}
	}
	if ct.dnc {
		return
	}
	for _, o := range ct.combos {
		if !o.ended && o.heapIdx < 0 {
			o.campaign.h.push(o)
		}
	}
}
