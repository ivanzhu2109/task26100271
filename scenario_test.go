package callsched

import "testing"

// 确定性剧本：联系人 P 同时属于活动 A 与 B，Q 只属于 A。
// 交错执行暂停、DNC 切换与失败提交，验证：
//   - 跨活动占用（P 被占用时 B 也取不到 P）；
//   - DNC/暂停期间可提交 Finish，解除后恢复原重试期限与尝试次数；
//   - 第三次失败永久终止组合；
//   - 可拨时刻恰好到期时可取，并列时刻按活动ID、联系人ID升序。
func TestScenarioCrossCampaignOccupancyAndRetry(t *testing.T) {
	s := New([]Membership{
		{CampaignID: "A", ContactID: "P"},
		{CampaignID: "A", ContactID: "Q"},
		{CampaignID: "B", ContactID: "P"},
	})

	mustTake := func(now int64, campaign, contact string) Ticket {
		t.Helper()
		c, ok := s.Take(now)
		if !ok {
			t.Fatalf("Take(%d): 期望取到 %s/%s，实际为空", now, campaign, contact)
		}
		if c.CampaignID != campaign || c.ContactID != contact {
			t.Fatalf("Take(%d): 期望 %s/%s，实际 %s/%s", now, campaign, contact, c.CampaignID, c.ContactID)
		}
		return c.Ticket
	}
	mustEmpty := func(now int64) {
		t.Helper()
		if c, ok := s.Take(now); ok {
			t.Fatalf("Take(%d): 期望为空，实际 %s/%s", now, c.CampaignID, c.ContactID)
		}
	}

	tkAP := mustTake(0, "A", "P") // A/P 与 A/Q 并列时刻 0，联系人ID P<Q
	tkAQ := mustTake(0, "A", "Q")
	mustEmpty(0) // P 被占用 => B/P 不可取（跨活动占用）

	s.Finish(tkAP, NoAnswer, 100) // A/P 第1次失败 => 可拨时刻 7300
	s.Finish(tkAQ, NoAnswer, 100) // A/Q 第1次失败 => 可拨时刻 7300

	tkBP := mustTake(200, "B", "P") // 仅 B/P@0 到期；A/P、A/Q 在 7300
	s.SetCampaignState("A", Paused)
	s.SetDoNotCall("P", true) // P 正在通话中，DNC 不撤销已开始的拨号
	mustEmpty(8000)           // A 暂停、P 占用中

	s.Finish(tkBP, NoAnswer, 300) // DNC+暂停期间提交 Finish：B/P 第1次失败 => 7500
	mustEmpty(9000)               // P 仍 DNC

	s.SetDoNotCall("P", false) // 解除屏蔽：A/P@7300(尝试1)、B/P@7500(尝试1) 原样恢复
	mustEmpty(7400)            // A 仍暂停（A/P@7300 被藏住），B/P@7500 未到期

	s.SetCampaignState("A", Active)
	tkAP = mustTake(7300, "A", "P") // 恰好到期可取；与 A/Q 并列 7300，P<Q
	s.Finish(tkAP, NoAnswer, 7300)  // A/P 第2次失败 => 14500

	tkAQ = mustTake(7300, "A", "Q")
	s.Finish(tkAQ, NoAnswer, 8000) // A/Q 第2次失败 => 15200

	tkBP = mustTake(7500, "B", "P") // B/P 恰好到期
	s.Finish(tkBP, NoAnswer, 7500)  // B/P 第2次失败 => 14700

	mustEmpty(14400)                 // A/P@14500、B/P@14700、A/Q@15200 均未到期
	tkAP = mustTake(14500, "A", "P") // A/P 第3次尝试
	s.Finish(tkAP, NoAnswer, 14500)  // 第3次失败 => 永久结束

	tkBP = mustTake(14700, "B", "P")
	s.Finish(tkBP, NoAnswer, 14700) // B/P 第3次失败 => 永久结束

	tkAQ = mustTake(15200, "A", "Q")
	s.Finish(tkAQ, NoAnswer, 15200) // A/Q 第3次失败 => 永久结束

	mustEmpty(1 << 40) // 全部结束，永远取空

	// 预算未被 DNC/暂停重置：每个组合恰好累计 3 次尝试。
	for _, ct := range s.contacts {
		for _, c := range ct.combos {
			if c.attempts != 3 || !c.ended {
				t.Errorf("组合 %s/%s: attempts=%d ended=%v，期望 3/true",
					c.campaign.id, c.contactID, c.attempts, c.ended)
			}
		}
	}
}

// Resolved 永久结束组合；DNC 期间提交 Resolved 同样生效。
func TestResolvedEndsCombo(t *testing.T) {
	s := New([]Membership{{CampaignID: "A", ContactID: "P"}})
	c, ok := s.Take(0)
	if !ok {
		t.Fatal("Take(0) 期望取到 A/P")
	}
	s.SetDoNotCall("P", true)
	s.Finish(c.Ticket, Resolved, 50) // DNC 期间提交 Resolved
	s.SetDoNotCall("P", false)
	if _, ok := s.Take(1 << 40); ok {
		t.Fatal("Resolved 后组合应永久结束")
	}
}
