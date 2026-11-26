package tests

import (
	"context"
	"strings"
	"testing"

	"doolla.ai/sdk"
)

func TestMetricAnswersNeedCurrentQuery(t *testing.T) {
	const missing = "未取得查询结果"
	for _, tc := range []struct {
		question, reply string
		needsSQL        bool
	}{
		{"本月良品率？", "98%", true},
		{"本月 OEE？", "综合设备效率为 0.97。", true},
		{"What was our first-pass yield this quarter?", "The first-pass yield was 92.8%.", true},
		{"这张表的外键和本月工单总数分别是多少？", "它有一个外键，共 2056 单。", true},
		{"List the foreign keys and how many orders shipped last month.", "There are 500 orders.", true},
		{"本月良品率的公式是什么？", "良品数除以总数，乘以 100%。", false},
		{"What is 49 / 50 as a percentage?", "98%", false},
		{"计算 49 ÷ 50 等于多少？", "98%", false},
		{"What is 98% of 100?", "98", false},
		{"什么是良品率？", "例如100个合格品中合格率为100%。", false},
		{"本月 OEE？", "请问要看 1 号线还是 2 号线？", false},
		{"表结构中有多少个外键？", "有 2 个外键。", false},
		{"记住的质检代码是什么？", "4051", false},
		{"本月 OEE？", "请问要看哪条产线？", false},
		{"请查询本月工单总数", "您指的是哪个车间？", false},
		{"本月 OEE？", "本月 OEE 为 98%。需要我继续吗？", true},
	} {
		t.Run(tc.question, func(t *testing.T) {
			attempts := 0
			model := testModel(func(_ context.Context, _ []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
				attempts++
				return doolla.Message{Content: tc.reply}, nil
			})
			agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{VerifyToolAnswers: true}, doolla.Registration{Tool: doolla.SQLTool{}})
			if err != nil {
				t.Fatal(err)
			}
			var answer string
			err = agent.Chat(t.Context(), doolla.User{ID: "test"}, "turn", tc.question, func(c doolla.Component) error {
				if c.Type == "text" {
					answer = c.Data["content"].(string)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.needsSQL {
				if attempts != 2 || !strings.Contains(answer, missing) {
					t.Errorf("got %q after %d model calls, wanted an unconfirmed query after one retry", answer, attempts)
				}
			} else if attempts != 1 || answer != tc.reply {
				t.Errorf("got %q after %d model calls, wanted %q directly", answer, attempts, tc.reply)
			}
		})
	}
}
