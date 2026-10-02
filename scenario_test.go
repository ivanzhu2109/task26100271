package dialsched

import "testing"

// 同一联系人属于两个活动,交错执行领取、暂停、DNC 切换与失败提交,
// 验证:跨活动占用、解除屏蔽后保留原重试期限、第三次失败终止、
// 以及时间恰好到期时按 (可拨时刻, 活动ID, 联系人ID) 排序。
func TestCrossCampaignScenario(t *testing.T) {
	s := NewScheduler(
		[]string{"c1"},
		[]string{"A", "B"},
		[]Membership{{"A", "c1"}, {"B", "c1"}},
	)

	mustTake := func(now int64, wantCamp string) Ticket {
		t.Helper()
		camp, cont, tk, ok := s.Take(now)
		if !ok {
			t.Fatalf("Take(%d) = empty, want %s/c1", now, wantCamp)
		}
		if camp != wantCamp || cont != "c1" {
			t.Fatalf("Take(%d) = %s/%s, want %s/c1", now, camp, cont, wantCamp)
		}
		return tk
	}
	mustEmpty := func(now int64) {
		t.Helper()
		if camp, cont, _, ok := s.Take(now); ok {
			t.Fatalf("Take(%d) = %s/%s, want empty", now, camp, cont)
		}
	}

	// t=0: 两组合同时到期,按活动 ID 升序取 A。
	tk1 := mustTake(0, "A")
	// 联系人被跨活动占用:B/c1 立即可见但不可取。
	mustEmpty(0)
	mustEmpty(50)

	// t=100: A/c1 第一次失败,下次可拨 100+7200=7300。
	s.Finish(tk1, NoAnswer, 100)
	// 释放后 B/c1(可拨时刻 0)立即可取,说明跨活动释放即时生效。
	tk2 := mustTake(100, "B")
	mustEmpty(100)

	// B 在拨期间暂停 B、并对 c1 打 DNC:均不撤销已开始的拨号。
	s.SetCampaignState("B", Paused)
	s.SetDoNotCall("c1", true)
	// 屏蔽期间仍可提交 Finish,结果在解除屏蔽后继续有效。
	s.Finish(tk2, NoAnswer, 200) // B/c1 下次可拨 200+7200=7400

	// DNC 未解除:即使 A 已到期也不可取。
	mustEmpty(8000)
	// 解除 DNC,但 A 仍因 B 暂停之外的原因……A 本身 Active,先恢复 B 之前验证 A 独立可选。
	s.SetDoNotCall("c1", false)
	// 原重试期限保留:7300 之前取不到。
	mustEmpty(7299)
	// 恰好到期时刻可取(不晚于 now),且 A(7300) 早于 B(7400)。
	tk3 := mustTake(7300, "A")
	mustEmpty(7300)               // B 未到期且联系人被占用
	s.Finish(tk3, NoAnswer, 7300) // A/c1 第 2 次失败,下次 14500

	// B 仍暂停:B/c1 到期也不参与。
	mustEmpty(8000)
	s.SetCampaignState("B", Active)
	// A(14500) 未到期,B(7400) 到期:取 B。
	tk4 := mustTake(8000, "B")
	s.Finish(tk4, NoAnswer, 8000) // B/c1 第 2 次失败,下次 15200

	// A 第 3 次失败 -> 永久结束。
	tk5 := mustTake(14500, "A")
	s.Finish(tk5, NoAnswer, 14500)
	if att, done, _ := s.ComboState("A", "c1"); att != 3 || !done {
		t.Fatalf("A/c1 attempts=%d done=%v, want 3/true", att, done)
	}
	// A 结束后 B 仍在 15200 等待。
	mustEmpty(15000)
	tk6 := mustTake(15200, "B")
	s.Finish(tk6, NoAnswer, 15200) // B/c1 第 3 次失败 -> 永久结束
	if att, done, _ := s.ComboState("B", "c1"); att != 3 || !done {
		t.Fatalf("B/c1 attempts=%d done=%v, want 3/true", att, done)
	}

	// 两组合均已结束,彻底无候选。
	mustEmpty(100000)
	st := s.Stats()
	if st.CampaignHeapItems != 0 || st.GlobalHeapItems != 0 || st.InFlight != 0 {
		t.Fatalf("Stats = %+v, want all heaps drained", st)
	}
}

// 解除 DNC 后尝试次数与可拨时刻不重置(预算不重新开始)。
func TestDncUnblockPreservesRetryState(t *testing.T) {
	s := NewScheduler(
		[]string{"x"},
		[]string{"C"},
		[]Membership{{"C", "x"}},
	)
	_, _, tk, ok := s.Take(0)
	if !ok {
		t.Fatal("initial Take empty")
	}
	s.Finish(tk, NoAnswer, 10) // 下次可拨 7210,已试 1 次

	s.SetDoNotCall("x", true)
	s.SetDoNotCall("x", false)

	if _, _, _, ok := s.Take(7209); ok {
		t.Fatal("Take(7209) succeeded, retry deadline was reset")
	}
	_, _, tk2, ok := s.Take(7210)
	if !ok {
		t.Fatal("Take(7210) empty, retry deadline not preserved")
	}
	s.Finish(tk2, Resolved, 7210)
	if att, done, _ := s.ComboState("C", "x"); att != 2 || !done {
		t.Fatalf("attempts=%d done=%v, want 2/true", att, done)
	}
}
