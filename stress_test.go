package dialsched

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// 10 万成员关系:大量更早到期组合被暂停、DNC 或占用,仅少量可拨;
// 反复切换同一大活动与同一联系人的 DNC 标记 10 万次,
// 验证选取结果、操作耗时与常驻状态量。
func TestStressScale(t *testing.T) {
	const nContacts = 20000 // 每个联系人 5 个成员关系 => N = 100000
	campaigns := []string{"C0", "C1", "C2", "C3", "C4"}
	contacts := make([]string, nContacts)
	for i := range contacts {
		contacts[i] = fmt.Sprintf("c%05d", i)
	}
	ms := make([]Membership, 0, nContacts*len(campaigns))
	for _, c := range contacts {
		for _, cp := range campaigns {
			ms = append(ms, Membership{cp, c})
		}
	}
	s := NewScheduler(contacts, campaigns, ms)
	if got := s.Stats().Combos; got != 100000 {
		t.Fatalf("Combos = %d, want 100000", got)
	}

	// ---- 阶段 1: 屏蔽绝大多数候选,仅留 3 个联系人可拨 ----
	for _, cp := range campaigns[1:] { // C1..C4 全部暂停(各 2 万成员)
		s.SetCampaignState(cp, Paused)
	}
	dialable := map[string]bool{"c00003": true, "c00005": true, "c00010": true}
	for _, c := range contacts {
		if !dialable[c] {
			s.SetDoNotCall(c, true)
		}
	}

	tkA := mustTakeStress(t, s, 0, "C0", "c00003")
	tkB := mustTakeStress(t, s, 0, "C0", "c00005")
	tkC := mustTakeStress(t, s, 0, "C0", "c00010")
	mustEmptyStress(t, s, 0) // 其余候选全部被暂停/DNC/占用挡住

	s.Finish(tkB, NoAnswer, 100) // c00005 下次可拨 7300
	mustEmptyStress(t, s, 200)   // 未到期,且不会逐项穿过屏蔽前缀
	tkB2 := mustTakeStress(t, s, 7300, "C0", "c00005")
	s.Finish(tkB2, Resolved, 7300)
	s.Finish(tkA, Resolved, 7300)
	s.Finish(tkC, Resolved, 7300)
	mustEmptyStress(t, s, 7300)

	// 屏蔽前缀下的空领取应近乎 O(1):测量 1 万次空 Take。
	start := time.Now()
	for i := 0; i < 10000; i++ {
		mustEmptyStress(t, s, 7300)
	}
	emptyTakeDur := time.Since(start)

	base := s.Stats()
	t.Logf("阶段1 空领取x10000 耗时 %v; 常驻状态 %+v", emptyTakeDur, base)

	// ---- 阶段 2: 同一大活动与同一联系人 DNC 各切换 10 万次 ----
	const toggles = 100000
	victim := "c00007" // 5 个成员关系
	start = time.Now()
	for i := 0; i < toggles; i++ {
		s.SetCampaignState("C1", Active)
		s.SetCampaignState("C1", Paused)
	}
	campDur := time.Since(start)
	start = time.Now()
	for i := 0; i < toggles; i++ {
		s.SetDoNotCall(victim, false)
		s.SetDoNotCall(victim, true)
	}
	dncDur := time.Since(start)

	after := s.Stats()
	t.Logf("阶段2 活动切换x%d 耗时 %v (%.0f ns/次), DNC切换x%d 耗时 %v (%.0f ns/次)",
		toggles, campDur, float64(campDur)/toggles, toggles, dncDur, float64(dncDur)/toggles)
	t.Logf("阶段2 后常驻状态 %+v", after)
	// 状态量不随切换次数膨胀:与阶段1 基线完全一致。
	if after != base {
		t.Fatalf("状态随切换膨胀: 前 %+v 后 %+v", base, after)
	}
	if after.CampaignHeapItems > after.Combos || after.GlobalHeapItems > len(campaigns) {
		t.Fatalf("状态越界: %+v", after)
	}

	// ---- 阶段 3: 全部解除屏蔽,按全局序耗尽所有组合,验证无遗漏 ----
	for _, cp := range campaigns[1:] {
		s.SetCampaignState(cp, Active)
	}
	for _, c := range contacts {
		s.SetDoNotCall(c, false)
	}

	type item struct {
		dialAt     int64
		camp, cont string
	}
	want := make([]item, 0, len(ms))
	for _, m := range ms {
		// 阶段 1 中这三个 C0 组合已 Resolved,永久结束,不再参与。
		if m.CampaignID == "C0" && dialable[m.ContactID] {
			continue
		}
		want = append(want, item{0, m.CampaignID, m.ContactID})
	}
	sort.Slice(want, func(i, j int) bool {
		a, b := want[i], want[j]
		if a.dialAt != b.dialAt {
			return a.dialAt < b.dialAt
		}
		if a.camp != b.camp {
			return a.camp < b.camp
		}
		return a.cont < b.cont
	})

	const farFuture = 1_000_000_000
	start = time.Now()
	for i, w := range want {
		camp, cont, tk, ok := s.Take(farFuture)
		if !ok {
			t.Fatalf("第 %d 次 Take 为空, 候选遗漏, 期望 %s/%s", i, w.camp, w.cont)
		}
		if camp != w.camp || cont != w.cont {
			t.Fatalf("第 %d 次 Take = %s/%s, 期望 %s/%s", i, camp, cont, w.camp, w.cont)
		}
		s.Finish(tk, Resolved, farFuture)
	}
	drainDur := time.Since(start)
	mustEmptyStress(t, s, farFuture)
	t.Logf("阶段3 耗尽 %d 个组合耗时 %v (%.0f ns/次)", len(want), drainDur, float64(drainDur)/float64(len(want)))

	final := s.Stats()
	t.Logf("结束后常驻状态 %+v", final)
	if final.CampaignHeapItems != 0 || final.GlobalHeapItems != 0 || final.InFlight != 0 {
		t.Fatalf("结束后状态未清空: %+v", final)
	}
}

func mustTakeStress(t *testing.T, s *Scheduler, now int64, wantCamp, wantCont string) Ticket {
	t.Helper()
	camp, cont, tk, ok := s.Take(now)
	if !ok || camp != wantCamp || cont != wantCont {
		t.Fatalf("Take(%d) = (%s,%s,%v), want %s/%s", now, camp, cont, ok, wantCamp, wantCont)
	}
	return tk
}

func mustEmptyStress(t *testing.T, s *Scheduler, now int64) {
	t.Helper()
	if camp, cont, _, ok := s.Take(now); ok {
		t.Fatalf("Take(%d) = %s/%s, want empty", now, camp, cont)
	}
}
