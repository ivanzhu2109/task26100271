package dialsched

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// refModel 是测试内的独立参照器:每次 Take 都全量扫描所有组合。
type refCombo struct {
	dialAt   int64
	attempts int
	done     bool
	inFlight bool
}

type refModel struct {
	combos   map[[2]string]*refCombo
	campaign map[string]CampaignState
	dnc      map[string]bool
	occupied map[string]bool
	inFlight map[Ticket][2]string
}

func newRefModel(campaigns, contacts []string, ms []Membership) *refModel {
	r := &refModel{
		combos:   map[[2]string]*refCombo{},
		campaign: map[string]CampaignState{},
		dnc:      map[string]bool{},
		occupied: map[string]bool{},
		inFlight: map[Ticket][2]string{},
	}
	for _, c := range campaigns {
		r.campaign[c] = Active
	}
	for _, m := range ms {
		r.combos[[2]string{m.CampaignID, m.ContactID}] = &refCombo{}
	}
	return r
}

func (r *refModel) take(now int64, tk Ticket) (string, string, bool) {
	type cand struct {
		camp, cont string
		dialAt     int64
	}
	var best *cand
	for key, cb := range r.combos {
		camp, cont := key[0], key[1]
		if cb.done || cb.inFlight || r.dnc[cont] || r.occupied[cont] ||
			r.campaign[camp] != Active || cb.dialAt > now {
			continue
		}
		c := cand{camp, cont, cb.dialAt}
		if best == nil || c.dialAt < best.dialAt ||
			(c.dialAt == best.dialAt && (c.camp < best.camp ||
				(c.camp == best.camp && c.cont < best.cont))) {
			best = &c
		}
	}
	if best == nil {
		return "", "", false
	}
	cb := r.combos[[2]string{best.camp, best.cont}]
	cb.attempts++
	cb.inFlight = true
	r.occupied[best.cont] = true
	r.inFlight[tk] = [2]string{best.camp, best.cont}
	return best.camp, best.cont, true
}

func (r *refModel) finish(tk Ticket, result Result, now int64) {
	key, ok := r.inFlight[tk]
	if !ok {
		panic("ref: bad ticket")
	}
	delete(r.inFlight, tk)
	cb := r.combos[key]
	cb.inFlight = false
	r.occupied[key[1]] = false
	if result == Resolved || cb.attempts >= maxAttempts {
		cb.done = true
	} else {
		cb.dialAt = now + retryDelay
	}
}

// 小规模随机操作序列,与全量扫描参照器逐步比较
// Take 返回结果与每个组合的剩余尝试状态。
func TestRandomAgainstReference(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		nContacts, nCampaigns := 1+rng.Intn(12), 1+rng.Intn(5)
		contacts := make([]string, nContacts)
		for i := range contacts {
			contacts[i] = fmt.Sprintf("ct%d", rng.Intn(2*nContacts)) // 含无序、非连续 ID
		}
		contacts = dedup(contacts)
		campaigns := make([]string, nCampaigns)
		for i := range campaigns {
			campaigns[i] = fmt.Sprintf("cp%d", i)
		}
		var ms []Membership
		seen := map[[2]string]bool{}
		for _, c := range contacts {
			k := 1 + rng.Intn(nCampaigns)
			perm := rng.Perm(nCampaigns)
			for j := 0; j < k; j++ {
				key := [2]string{campaigns[perm[j]], c}
				if !seen[key] {
					seen[key] = true
					ms = append(ms, Membership{key[0], key[1]})
				}
			}
		}

		s := NewScheduler(contacts, campaigns, ms)
		ref := newRefModel(campaigns, contacts, ms)
		var now int64
		var open []Ticket

		for step := 0; step < 3000; step++ {
			switch op := rng.Intn(100); {
			case op < 40: // Take
				now += int64(rng.Intn(3))
				gc, gt, tk, ok := s.Take(now)
				rc, rt, rok := ref.take(now, tk)
				if ok != rok || gc != rc || gt != rt {
					t.Fatalf("seed=%d step=%d Take(%d): got (%s,%s,%v), ref (%s,%s,%v)",
						seed, step, now, gc, gt, ok, rc, rt, rok)
				}
				if ok {
					open = append(open, tk)
				}
			case op < 70: // Finish
				if len(open) == 0 {
					continue
				}
				i := rng.Intn(len(open))
				tk := open[i]
				open = append(open[:i], open[i+1:]...)
				res := Resolved
				if rng.Intn(2) == 0 {
					res = NoAnswer
				}
				now += int64(rng.Intn(3))
				s.Finish(tk, res, now)
				ref.finish(tk, res, now)
			case op < 85: // 活动切换
				c := campaigns[rng.Intn(len(campaigns))]
				st := Paused
				if rng.Intn(2) == 0 {
					st = Active
				}
				s.SetCampaignState(c, st)
				ref.campaign[c] = st
			default: // DNC 切换
				c := contacts[rng.Intn(len(contacts))]
				b := rng.Intn(2) == 0
				s.SetDoNotCall(c, b)
				ref.dnc[c] = b
			}

			// 每 50 步全量核对一次所有组合的尝试次数与结束标记。
			if step%50 == 0 {
				for key, rc := range ref.combos {
					att, done, exists := s.ComboState(key[0], key[1])
					if !exists || att != rc.attempts || done != rc.done {
						t.Fatalf("seed=%d step=%d combo %v: got (%d,%v,%v), ref (%d,%v)",
							seed, step, key, att, done, exists, rc.attempts, rc.done)
					}
				}
			}
		}
	}
}

func dedup(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}
