package dialsched

// combo 是一条成员关系(活动 x 联系人)的调度状态。
type combo struct {
	campaign int   // 所属活动下标
	contact  int   // 所属联系人下标
	dialAt   int64 // 下次可拨时刻(秒)
	attempts int   // 已消耗尝试次数
	done     bool  // 组合已永久结束
	inFlight bool  // 本组合有一次未完成的拨号
	inHeap   bool  // 当前是否驻留在所属活动堆中
	heapPos  int   // 在所属活动堆中的位置,不在堆中为 -1
}

// campaign 是一个活动及其候选组合堆。
type campaign struct {
	id    string
	state CampaignState
	heap  intHeap // 该活动内"联系人未 DNC、未占用、组合未结束、未在拨"的组合
	gpos  int     // 在全局堆中的位置,不在其中为 -1
}

// contact 是一个联系人的全局状态。
type contact struct {
	id       string
	dnc      bool  // 全局拒接标记
	occupied bool  // 已有一次未完成拨号(跨所有活动唯一)
	combos   []int // 该联系人的成员关系下标,k <= 10
}

// Scheduler 是外呼名单调度器。非并发安全,仅供单进程串行调用。
//
// 结构为两级堆:每个活动维护一个候选组合堆(与活动是否暂停无关),
// 全局堆只装 Active 活动的堆首。不变式:组合驻留在其活动堆中,当且仅当
// 组合未结束、未在拨,且其联系人未 DNC、未被占用。
// 暂停/恢复活动只需从全局堆删/插一个活动堆首,不触碰任何成员。
type Scheduler struct {
	combos    []combo
	campaigns []campaign
	contacts  []contact

	global      intHeap // 各 Active 活动的堆首,按 (dialAt, campaignID, contactID) 排序
	campaignIdx map[string]int
	contactIdx  map[string]int

	tickets    map[Ticket]int // 未完成拨号:票据 -> 组合下标
	nextTicket Ticket
}

// NewScheduler 以固定的联系人、活动及成员关系初始化调度器。
// 活动初始均为 Active,每个组合的可拨时刻为 0、尝试次数为 0。
// 活动数不得超过 10;未知 ID、重复 ID 或重复成员关系会导致 panic。
func NewScheduler(contactIDs, campaignIDs []string, memberships []Membership) *Scheduler {
	if len(campaignIDs) == 0 || len(campaignIDs) > 10 {
		panic("dialsched: campaign count must be in [1, 10]")
	}
	s := &Scheduler{
		campaigns:   make([]campaign, len(campaignIDs)),
		contacts:    make([]contact, len(contactIDs)),
		campaignIdx: make(map[string]int, len(campaignIDs)),
		contactIdx:  make(map[string]int, len(contactIDs)),
		tickets:     make(map[Ticket]int),
	}
	for i, id := range campaignIDs {
		if _, dup := s.campaignIdx[id]; dup {
			panic("dialsched: duplicate campaign id " + id)
		}
		s.campaignIdx[id] = i
		s.campaigns[i] = campaign{id: id, state: Active, gpos: -1}
	}
	for i, id := range contactIDs {
		if _, dup := s.contactIdx[id]; dup {
			panic("dialsched: duplicate contact id " + id)
		}
		s.contactIdx[id] = i
		s.contacts[i] = contact{id: id}
	}

	seen := make(map[[2]int]struct{}, len(memberships))
	s.combos = make([]combo, len(memberships))
	for i, m := range memberships {
		ci, ok := s.campaignIdx[m.CampaignID]
		if !ok {
			panic("dialsched: unknown campaign id " + m.CampaignID)
		}
		ct, ok := s.contactIdx[m.ContactID]
		if !ok {
			panic("dialsched: unknown contact id " + m.ContactID)
		}
		key := [2]int{ci, ct}
		if _, dup := seen[key]; dup {
			panic("dialsched: duplicate membership " + m.CampaignID + "/" + m.ContactID)
		}
		seen[key] = struct{}{}
		s.combos[i] = combo{campaign: ci, contact: ct, heapPos: -1}
		s.contacts[ct].combos = append(s.contacts[ct].combos, i)
		s.campaigns[ci].heap.items = append(s.campaigns[ci].heap.items, i)
	}

	// 活动堆按 (dialAt, contactID) 排序;同堆内活动 ID 相同,无需比较。
	for ci := range s.campaigns {
		s.campaigns[ci].heap.less = func(a, b int) bool { return s.comboLess(a, b) }
		s.campaigns[ci].heap.setPos = func(id, pos int) { s.combos[id].heapPos = pos }
		s.campaigns[ci].heap.heapify()
	}
	// 全局堆按 (dialAt, campaignID, contactID) 比较各活动堆首。
	s.global.less = func(a, b int) bool { return s.campaignLess(a, b) }
	s.global.setPos = func(id, pos int) { s.campaigns[id].gpos = pos }
	// 初始时所有组合均合格:标记入堆并构建全局堆。
	for i := range s.combos {
		s.combos[i].inHeap = true
	}
	for ci := range s.campaigns {
		if s.campaigns[ci].heap.len() > 0 {
			s.global.items = append(s.global.items, ci)
		}
	}
	s.global.heapify()
	return s
}

// comboLess 比较两个组合的可拨次序:(dialAt, contactID) 升序。
// 调用方保证两组合属于同一活动。
func (s *Scheduler) comboLess(a, b int) bool {
	ca, cb := &s.combos[a], &s.combos[b]
	if ca.dialAt != cb.dialAt {
		return ca.dialAt < cb.dialAt
	}
	return s.contacts[ca.contact].id < s.contacts[cb.contact].id
}

// campaignLess 比较两个活动的堆首组合:(dialAt, campaignID, contactID) 升序。
func (s *Scheduler) campaignLess(a, b int) bool {
	ha, hb := s.campaigns[a].heap.top(), s.campaigns[b].heap.top()
	ca, cb := &s.combos[ha], &s.combos[hb]
	if ca.dialAt != cb.dialAt {
		return ca.dialAt < cb.dialAt
	}
	if s.campaigns[a].id != s.campaigns[b].id {
		return s.campaigns[a].id < s.campaigns[b].id
	}
	return s.contacts[ca.contact].id < s.contacts[cb.contact].id
}

// refreshCampaign 在活动堆内容或活动状态变化后,修正全局堆中该活动的条目。
func (s *Scheduler) refreshCampaign(ci int) {
	c := &s.campaigns[ci]
	eligible := c.state == Active && c.heap.len() > 0
	switch {
	case eligible && c.gpos < 0:
		s.global.push(ci)
	case eligible:
		s.global.fix(c.gpos)
	case !eligible && c.gpos >= 0:
		s.global.removeAt(c.gpos)
	}
}

// addToHeap 在组合满足驻留条件时把它放回所属活动堆。
func (s *Scheduler) addToHeap(mi int) {
	cb := &s.combos[mi]
	if cb.inHeap || cb.done || cb.inFlight {
		return
	}
	ct := &s.contacts[cb.contact]
	if ct.dnc || ct.occupied {
		return
	}
	cb.inHeap = true
	s.campaigns[cb.campaign].heap.push(mi)
	s.refreshCampaign(cb.campaign)
}

// removeFromHeap 把组合从所属活动堆中摘除(若在其中)。
func (s *Scheduler) removeFromHeap(mi int) {
	cb := &s.combos[mi]
	if !cb.inHeap {
		return
	}
	cb.inHeap = false
	s.campaigns[cb.campaign].heap.removeAt(cb.heapPos)
	s.refreshCampaign(cb.campaign)
}

// SetCampaignState 在 Active 与 Paused 之间切换活动,最坏 O(log N)。
// 只增删全局堆中的一个活动条目,不遍历活动成员。
func (s *Scheduler) SetCampaignState(id string, state CampaignState) {
	ci, ok := s.campaignIdx[id]
	if !ok {
		panic("dialsched: unknown campaign id " + id)
	}
	c := &s.campaigns[ci]
	if c.state == state {
		return
	}
	c.state = state
	s.refreshCampaign(ci)
}

// SetDoNotCall 设置或解除联系人的全局 DNC 标记,摊还 O(k log N)。
// 只影响后续领取;解除后组合按原有可拨时刻与尝试次数恢复。
func (s *Scheduler) SetDoNotCall(contactID string, blocked bool) {
	ci, ok := s.contactIdx[contactID]
	if !ok {
		panic("dialsched: unknown contact id " + contactID)
	}
	ct := &s.contacts[ci]
	if ct.dnc == blocked {
		return
	}
	ct.dnc = blocked
	for _, m := range ct.combos {
		if blocked {
			s.removeFromHeap(m)
		} else {
			s.addToHeap(m)
		}
	}
}

// Take 领取一个当前可拨的组合,立即占用联系人并消耗一次尝试。
// 返回活动 ID、联系人 ID 与本次拨号票据;没有候选时 ok 为 false。
// 摊还 O(k log N)。
func (s *Scheduler) Take(now int64) (campaignID, contactID string, ticket Ticket, ok bool) {
	if s.global.len() == 0 {
		return "", "", 0, false
	}
	ci := s.global.top()
	mi := s.campaigns[ci].heap.top()
	cb := &s.combos[mi]
	if cb.dialAt > now {
		return "", "", 0, false
	}
	ct := &s.contacts[cb.contact]
	// 占用联系人:把该联系人的全部组合(含本组合)从各活动堆摘除,
	// 使跨活动的其他组合立即退出选择。
	for _, m := range ct.combos {
		s.removeFromHeap(m)
	}
	ct.occupied = true
	cb.inFlight = true
	cb.attempts++
	s.nextTicket++
	t := s.nextTicket
	s.tickets[t] = mi
	return s.campaigns[ci].id, ct.id, t, true
}

// Finish 结束一次拨号并释放联系人,摊还 O(k log N)。每张票据仅可提交一次。
// Resolved 或累计尝试达到上限时组合永久结束;否则下次可拨时刻为 now+7200。
// 联系人释放后,其在各活动的组合立即重新参与选择(若未 DNC)。
func (s *Scheduler) Finish(ticket Ticket, result Result, now int64) {
	mi, ok := s.tickets[ticket]
	if !ok {
		panic("dialsched: unknown or already finished ticket")
	}
	delete(s.tickets, ticket)
	cb := &s.combos[mi]
	ct := &s.contacts[cb.contact]
	cb.inFlight = false
	ct.occupied = false
	if result == Resolved || cb.attempts >= maxAttempts {
		cb.done = true
	} else {
		cb.dialAt = now + retryDelay
	}
	for _, m := range ct.combos {
		s.addToHeap(m)
	}
}

// Stats 返回当前常驻状态量。
func (s *Scheduler) Stats() Stats {
	st := Stats{
		Combos:          len(s.combos),
		GlobalHeapItems: s.global.len(),
		InFlight:        len(s.tickets),
	}
	for i := range s.campaigns {
		st.CampaignHeapItems += s.campaigns[i].heap.len()
	}
	return st
}

// ComboState 返回某组合的尝试次数与是否已结束,供测试核对。
func (s *Scheduler) ComboState(campaignID, contactID string) (attempts int, done, exists bool) {
	ci, ok := s.campaignIdx[campaignID]
	if !ok {
		return 0, false, false
	}
	ct, ok := s.contactIdx[contactID]
	if !ok {
		return 0, false, false
	}
	for _, m := range s.contacts[ct].combos {
		if s.combos[m].campaign == ci {
			return s.combos[m].attempts, s.combos[m].done, true
		}
	}
	return 0, false, false
}
