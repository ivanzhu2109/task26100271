package callsched

import (
	"math/rand"
	"testing"
)

// refScheduler 是测试专用的独立参照器：每次 Take 全量扫描所有组合，
// 不做任何索引，语义直接来自需求描述。
type refScheduler struct {
	campaigns map[string]bool // id -> active
	dnc       map[string]bool
	occupied  map[string]bool
	combos    []*refCombo
	tickets   map[Ticket]*refCombo
	next      Ticket
}

type refCombo struct {
	campaign, contact string
	dialableAt        int64
	attempts          int
	ended             bool
}

func newRef(ms []Membership) *refScheduler {
	r := &refScheduler{
		campaigns: map[string]bool{},
		dnc:       map[string]bool{},
		occupied:  map[string]bool{},
		tickets:   map[Ticket]*refCombo{},
	}
	seen := map[[2]string]bool{}
	for _, m := range ms {
		r.campaigns[m.CampaignID] = true
		key := [2]string{m.CampaignID, m.ContactID}
		if !seen[key] {
			seen[key] = true
			r.combos = append(r.combos, &refCombo{campaign: m.CampaignID, contact: m.ContactID})
		}
	}
	return r
}

func (r *refScheduler) take(now int64) (campaign, contact string, tk Ticket, ok bool) {
	var best *refCombo
	for _, c := range r.combos {
		if !r.campaigns[c.campaign] || r.dnc[c.contact] || r.occupied[c.contact] ||
			c.ended || c.dialableAt > now {
			continue
		}
		if best == nil ||
			c.dialableAt < best.dialableAt ||
			(c.dialableAt == best.dialableAt && (c.campaign < best.campaign ||
				(c.campaign == best.campaign && c.contact < best.contact))) {
			best = c
		}
	}
	if best == nil {
		return "", "", 0, false
	}
	best.attempts++
	r.occupied[best.contact] = true
	tk = r.next
	r.next++
	r.tickets[tk] = best
	return best.campaign, best.contact, tk, true
}

func (r *refScheduler) finish(tk Ticket, result Result, now int64) {
	c, ok := r.tickets[tk]
	if !ok {
		return
	}
	delete(r.tickets, tk)
	r.occupied[c.contact] = false
	switch result {
	case Resolved:
		c.ended = true
	case NoAnswer:
		if c.attempts < 3 {
			c.dialableAt = now + 7200
		} else {
			c.ended = true
		}
	}
}

// TestRandomAgainstReference 用小规模随机操作序列逐步对比调度器与参照器：
// Take 返回结果一致，且每个组合的剩余尝试状态一致。
func TestRandomAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261002))
	campaignIDs := []string{"c0", "c1", "c2"}
	contactIDs := []string{"u0", "u1", "u2", "u3", "u4", "u5", "u6", "u7"}

	var ms []Membership
	for _, cid := range contactIDs {
		for _, cp := range campaignIDs {
			if rng.Intn(2) == 0 { // 随机成员关系，k 最多 3
				ms = append(ms, Membership{CampaignID: cp, ContactID: cid})
			}
		}
	}
	if len(ms) == 0 {
		ms = append(ms, Membership{CampaignID: "c0", ContactID: "u0"})
	}

	s := New(ms)
	ref := newRef(ms)
	var now int64
	open := map[Ticket]bool{} // 双方共享票据编号（初始均为 0 起同步递增）

	for step := 0; step < 4000; step++ {
		now += int64(rng.Intn(4000)) // 时间单调不减
		switch rng.Intn(10) {
		case 0, 1: // 切换活动状态
			cp := campaignIDs[rng.Intn(len(campaignIDs))]
			st := Active
			if rng.Intn(2) == 0 {
				st = Paused
			}
			s.SetCampaignState(cp, st)
			ref.campaigns[cp] = st == Active
		case 2, 3: // 切换 DNC
			ct := contactIDs[rng.Intn(len(contactIDs))]
			b := rng.Intn(2) == 0
			s.SetDoNotCall(ct, b)
			ref.dnc[ct] = b
		case 4, 5, 6: // Finish 一张未结票据
			if len(open) > 0 {
				var tk Ticket
				for tk = range open {
					break
				}
				delete(open, tk)
				res := Resolved
				if rng.Intn(2) == 0 {
					res = NoAnswer
				}
				s.Finish(tk, res, now)
				ref.finish(tk, res, now)
			}
		default: // Take
			got, ok1 := s.Take(now)
			rc, rt, rtk, ok2 := ref.take(now)
			if ok1 != ok2 {
				t.Fatalf("step %d: Take 一致性失败 got ok=%v ref ok=%v", step, ok1, ok2)
			}
			if ok1 {
				if got.CampaignID != rc || got.ContactID != rt || got.Ticket != rtk {
					t.Fatalf("step %d: Take=%s/%s#%d, ref=%s/%s#%d",
						step, got.CampaignID, got.ContactID, got.Ticket, rc, rt, rtk)
				}
				open[got.Ticket] = true
			}
		}

		// 每 200 步全量比对一次组合状态（尝试次数、结束标记、可拨时刻）。
		if step%200 == 0 {
			want := map[[2]string]*refCombo{}
			for _, c := range ref.combos {
				want[[2]string{c.campaign, c.contact}] = c
			}
			for _, ct := range s.contacts {
				for _, c := range ct.combos {
					w := want[[2]string{c.campaign.id, c.contactID}]
					if w == nil {
						t.Fatalf("step %d: 调度器多出组合 %s/%s", step, c.campaign.id, c.contactID)
					}
					if c.attempts != w.attempts || c.ended != w.ended || c.dialableAt != w.dialableAt {
						t.Fatalf("step %d: 组合 %s/%s 状态 (%d,%v,%d)，参照 (%d,%v,%d)",
							step, c.campaign.id, c.contactID,
							c.attempts, c.ended, c.dialableAt,
							w.attempts, w.ended, w.dialableAt)
					}
				}
			}
		}
	}
}
