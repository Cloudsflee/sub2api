package productselection

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func gptReport() *SolutionReport {
	at := "2026-11-22T12:00:00Z"
	r := &SolutionReport{Schema: 1, Version: "pilot", AnalyzedAt: at,
		Snapshot: Snapshot{At: at, Source: "saved catalog fixture", Count: 1, Shops: 1, SHA256: digest([]byte("fixture")), IdentitySHA256: digest([]byte("shop/offer"))},
		Offers: []Offer{{ID: "offer", ShopID: "shop", Shop: "Example", Name: "原号订阅", URL: "https://example.com/item/offer", Type: "订阅", Minimum: 1,
			DetailState: "read", DetailAt: at, Original: "<script>only text</script> 仅提供兑换", Disposition: "component", Reason: "自有账号兑换路径"}},
		Evidence: []Evidence{{ID: "plus", Kind: "official", Title: "ChatGPT Plus", URL: "https://help.openai.com/en/articles/6950777-what-is-chatgpt-plus", At: at, Fact: "Plus 为网页及 App 订阅，API 单独计费。"}},
		Solutions: []Solution{{ID: "gpt", GoalKey: "personal-ai", PathKey: "redeem-own-account", Name: "自有账号使用 GPT", Purpose: "在现有个人账号运行第一项文件任务",
			Customer: "需要提高文件处理额度的个人用户", Difference: "在客户自有账号兑换并确认权益与到期日", Prerequisite: "客户本人能登录并控制恢复邮箱", Gap: "兑换适用地区与工时待验证",
			Stage: "priority", Priority: 1, Delivery: "untested", Channels: []string{"private"},
			Journey: Journey{"确认账号归属和地区", "客户自行兑换", "检查订阅页并运行文件任务", "查看购买记录与兑换状态", "在原计费入口确认续费"},
			Components: []Component{{ID: "subscription", Role: "required", Kind: "product", Name: "个人订阅", Status: "merchant", Condition: "已登录自有账号", Offers: []string{"offer"}},
				{ID: "support", Role: "optional", Kind: "service", Name: "订阅状态排查", Status: "untested", Condition: "支付后计划未更新", DependsOn: []string{"subscription"}}},
			Problems:   []Problem{{Trigger: "兑换后未显示权益", Check: "核对账号与订单", Action: "客户查看原支付凭证和订阅页", Fallback: "联系原计费方并停止重复购买", Component: "support", Status: "untested", Offer: "offer"}},
			Commercial: Commercial{"Plus 月付20美元；先试免费档", "套餐辨识与配置省时，客户可能已会操作", "私域先按具体文件需求访谈", "到期复核额度后再续", "报价及人工成本未知", "由本人保管登录与恢复信息"},
			Evidence:   []string{"plus"}}}}
	return r
}

func TestSolutionReportCoverageAndCost(t *testing.T) {
	r := gptReport()
	require.NoError(t, r.Validate())
	r.Solutions = append(r.Solutions, r.Solutions[0])
	r.Solutions[1].ID = "other-shop"
	require.ErrorContains(t, r.Validate(), "duplicate")
	r.Solutions = r.Solutions[:1]
	r.Solutions[0].Components[1].Role = "required"
	r.Solutions[0].Components[1].Status = "missing"
	require.NoError(t, r.Validate())
	require.Equal(t, 1, r.Audit().Incomplete)
	require.Zero(t, r.Audit().Valid)
	require.Nil(t, summarize(r.Solutions[0], r.references()).BaseCost)
	r.Solutions[0].Components[0].Offers = []string{"absent"}
	require.ErrorContains(t, r.Validate(), "invalid product reference")
	r = gptReport()
	r.Offers[0].Disposition = "pending"
	require.ErrorContains(t, r.Validate(), "disposition")
	r = gptReport()
	r.Offers[0].Cost = new(float64)
	require.ErrorContains(t, r.Validate(), "invalid offer")
	r = gptReport()
	r.Solutions[0].Components[0].DependsOn = []string{"support"}
	require.ErrorContains(t, r.Validate(), "cyclic")
}

func TestProductChoicesFollowSourcesAndOperatorEdits(t *testing.T) {
	l := Library{Dir: t.TempDir()}
	r := gptReport()
	_, err := l.Import(r)
	require.NoError(t, err)
	// Reproduce an index written before choices were stored.
	err = l.mutate(func(idx *libraryIndex) (bool, error) {
		e := idx.Entries["gpt"]
		e.Summary.Choices = nil
		idx.Entries["gpt"] = e
		return true, nil
	})
	require.NoError(t, err)
	list, err := l.List()
	require.NoError(t, err)
	require.Len(t, list.Solutions[0].Choices, 1)
	require.Equal(t, r.Offers[0].Name, list.Solutions[0].Choices[0].Name)
	d, err := l.Detail("gpt")
	require.NoError(t, err)
	d.Solution.Components[0].Solves = "免费额度不足时处理文件"
	d.Solution.Assembly = "开通原号，再用客户的样本验收。"
	require.NoError(t, l.Edit("gpt", SolutionEdit{Revision: d.Revision, Solution: d.Solution}))
	list, err = l.List()
	require.NoError(t, err)
	require.Equal(t, d.Solution.Assembly, list.Solutions[0].Assembly)
	require.Equal(t, []ProductChoice{{OfferID: "offer", Name: "个人订阅", Listing: "原号订阅", Role: "required",
		Solves: "免费额度不足时处理文件", Condition: "已登录自有账号", Status: "merchant"}}, list.Solutions[0].Choices)
	base := d.Solution
	d.Solution.Assembly = ""
	edited, err := applyEdits(base, differences(base, d.Solution))
	require.NoError(t, err)
	require.Empty(t, edited.Assembly)
}

func TestSolutionLibraryAtomicImportEditAndConflict(t *testing.T) {
	l := Library{Dir: t.TempDir()}
	r := gptReport()
	ok, err := l.Import(r)
	require.NoError(t, err)
	require.True(t, ok)
	d, err := l.Detail("gpt")
	require.NoError(t, err)
	d.Solution.Name = "人工标题"
	require.NoError(t, l.Edit("gpt", SolutionEdit{Revision: d.Revision, Solution: d.Solution, Notes: "保留备注"}))
	require.ErrorIs(t, l.Edit("gpt", SolutionEdit{Revision: d.Revision, Solution: d.Solution}), ErrConflict)
	ok, err = l.Import(r)
	require.NoError(t, err)
	require.False(t, ok)
	r.Solutions[0].Name = "新版标题"
	_, err = l.Import(r)
	require.ErrorIs(t, err, ErrConflict)
	r.Version = "second"
	r.AnalyzedAt = "2026-11-23T04:00:00Z"
	r.Solutions[0].Gap = "更新后的证据缺口"
	l.write = func(path string, data []byte) error {
		if filepath.Base(path) == "library.json" {
			return errors.New("disk full")
		}
		return atomicWrite(path, data)
	}
	_, err = l.Import(r)
	require.Error(t, err)
	list, err := l.List()
	require.NoError(t, err)
	require.Equal(t, "pilot", list.Version)
	require.Equal(t, "人工标题", list.Solutions[0].Name)
	l.write = nil
	ok, err = l.Import(r)
	require.NoError(t, err)
	require.True(t, ok)
	d, err = l.Detail("gpt")
	require.NoError(t, err)
	require.Equal(t, "保留备注", d.Notes)
	require.Equal(t, "人工标题", d.Solution.Name)
	require.Equal(t, "更新后的证据缺口", d.Solution.Gap)
	require.Equal(t, []string{"name"}, d.Conflicts)
	require.Equal(t, "新版标题", d.Incoming.Name)
	before, err := l.ActiveReport("pilot")
	require.NoError(t, err)
	require.Equal(t, "自有账号使用 GPT", before.Solutions[0].Name)
	require.NoError(t, l.Edit("gpt", SolutionEdit{Revision: d.Revision, Solution: d.Solution, Notes: d.Notes}))
	list, err = l.List()
	require.NoError(t, err)
	require.False(t, list.Solutions[0].Review)
}
