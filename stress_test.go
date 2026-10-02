package callsched

import (
	"fmt"
	"testing"
	"time"
)

// TestStressBlockedPrefixAndToggles 压力测试：
//   - 10 个活动 × 1 万联系人 = 10 万成员关系（k=10）；
//   - 构造大量"更早到期"组合分别被暂停、DNC、占用挡住，仅留少量可拨；
//   - 反复切换同一大活动与同一联系人 DNC 各 10 万次；
//   - 验证选取结果、操作耗时与常驻状态量（不随切换次数膨胀）。
func TestStressBlockedPrefixAndToggles(t *testing.T) {
	const campaigns, contacts = 10, 10000
	ms := make([]Membership, 0, campaigns*contacts)
	for c := 0; c < campaigns; c++ {
		for u := 0; u < contacts; u++ {
			ms = append(ms, Membership{
				CampaignID: fmt.Sprintf("c%d", c),
				ContactID:  fmt.Sprintf("u%05d", u),
			})
		}
	}

	start := time.Now()
	s := New(ms)
	t.Logf("初始化 %d 条成员关系: %v", len(ms), time.Since(start))

	// 重试波次：c0 全部组合进入 7200 秒后的重试，使 c1..c9 的 0 时刻组合成为"更早到期"。
	start = time.Now()
	tickets := make([]Ticket, 0, contacts)
	for i := 0; i < contacts; i++ {
		call, ok := s.Take(0)
		if !ok {
			t.Fatalf("重试波次第 %d 次 Take 意外为空", i)
		}
		tickets = append(tickets, call.Ticket)
	}
	for _, tk := range tickets {
		s.Finish(tk, NoAnswer, 0) // c0/* => dialableAt=7200
	}
	tickets = tickets[:0]
	t.Logf("重试波次（1 万次 Take+Finish）: %v", time.Since(start))

	// 屏蔽前缀：暂停 c1..c8（8 万个 0 时刻组合），DNC 前 9000 个联系人（9 万个组合）。
	for c := 1; c <= 8; c++ {
		s.SetCampaignState(fmt.Sprintf("c%d", c), Paused)
	}
	for u := 0; u < 9000; u++ {
		s.SetDoNotCall(fmt.Sprintf("u%05d", u), true)
	}

	// 占用 100 个联系人（持有票据不释放）。
	for i := 0; i < 100; i++ {
		call, ok := s.Take(0)
		if !ok {
			t.Fatalf("占用波次第 %d 次 Take 意外为空", i)
		}
		if call.CampaignID != "c9" {
			t.Fatalf("占用波次期望从 c9 取号，实际 %s", call.CampaignID)
		}
		tickets = append(tickets, call.Ticket)
	}

	// 此时可拨集合 = c9 中 u09100..u09999（900 个，时刻 0）；
	// 更早到期的 9.9 万组合分别被暂停/DNC/占用挡住。
	start = time.Now()
	const probeRounds = 900 // 可拨集合恰好 900 个（c9 的 u09100..u09999）
	for i := 0; i < probeRounds; i++ {
		call, ok := s.Take(0)
		if !ok {
			t.Fatalf("第 %d 轮探测 Take 意外为空", i)
		}
		want := fmt.Sprintf("u%05d", 9100+i)
		if call.CampaignID != "c9" || call.ContactID != want {
			t.Fatalf("第 %d 轮探测期望 c9/%s，实际 %s/%s", i, want, call.CampaignID, call.ContactID)
		}
		s.Finish(call.Ticket, NoAnswer, 0) // => 7200，让位给下一个
	}
	t.Logf("%d 轮 Take+Finish（每轮穿过 9.9 万被屏蔽的更早到期前缀）: %v", probeRounds, time.Since(start))

	// 反复切换同一大活动（c1，1 万成员）与同一联系人 DNC，各 10 万次。
	start = time.Now()
	const toggles = 100000
	for i := 0; i < toggles; i++ {
		s.SetCampaignState("c1", Paused)
		s.SetCampaignState("c1", Active)
		s.SetDoNotCall("u09500", true)
		s.SetDoNotCall("u09500", false)
	}
	elapsed := time.Since(start)
	t.Logf("%d 轮活动暂停/恢复 + DNC 屏蔽/解除（共 %d 次状态切换）: %v（%.0f 次/秒）",
		toggles, toggles*4, elapsed, float64(toggles*4)/elapsed.Seconds())

	// 切换不遗漏候选：c1 恢复 Active 后，其 u09100 组合（时刻 0）成为全局最小。
	call, ok := s.Take(0)
	if !ok || call.CampaignID != "c1" || call.ContactID != "u09100" {
		t.Fatalf("切换后期望取到 c1/u09100，实际 %+v ok=%v", call, ok)
	}
	s.Finish(call.Ticket, Resolved, 0)

	// 常驻状态量：堆中元素 + 被占用/屏蔽而暂离堆的组合 = N，票据数 <= 占用联系人数。
	inHeaps := 0
	for _, cp := range s.campaigns {
		inHeaps += cp.h.Len()
	}
	offHeap := 0
	total := 0
	for _, ct := range s.contacts {
		for _, c := range ct.combos {
			total++
			if c.heapIdx < 0 {
				offHeap++
			}
		}
	}
	if total != len(ms) {
		t.Fatalf("组合总数 %d，期望 %d", total, len(ms))
	}
	if inHeaps+offHeap != total {
		t.Fatalf("堆内 %d + 堆外 %d != 总数 %d", inHeaps, offHeap, total)
	}
	if len(s.tickets) != 100 {
		t.Fatalf("未结票据数 %d，期望 100", len(s.tickets))
	}
	t.Logf("常驻状态: 组合 %d（堆内 %d / 堆外 %d），联系人 %d，未结票据 %d；10 万次切换后无增长",
		total, inHeaps, offHeap, len(s.contacts), len(s.tickets))
}
