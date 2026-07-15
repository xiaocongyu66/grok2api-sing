package tokencount

import "testing"

func TestEstimateTextLatinCJK(t *testing.T) {
	if got := EstimateText(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	latin := EstimateText("hello world this is a longer prompt for tokens")
	if latin < 8 || latin > 20 {
		t.Fatalf("latin estimate = %d", latin)
	}
	cjk := EstimateText("你好世界这是一段中文提示词用于估算")
	if cjk < 10 {
		t.Fatalf("cjk estimate too low: %d", cjk)
	}
	// CJK should be denser than Latin of similar byte length.
	if cjk <= latin/2 {
		t.Fatalf("cjk=%d should exceed half of latin=%d", cjk, latin)
	}
}

func TestEstimateRequestBodyIgnoresInlineMediaExplosion(t *testing.T) {
	small := EstimateRequestBody([]byte(`{"input":"hello","max_output_tokens":1000}`))
	large := EstimateRequestBody([]byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,` + stringsRepeat("A", 4000) + `"}]}`))
	if large > small+400 {
		t.Fatalf("media estimate exploded: small=%d large=%d", small, large)
	}
}

func stringsRepeat(value string, n int) string {
	out := make([]byte, 0, len(value)*n)
	for range n {
		out = append(out, value...)
	}
	return string(out)
}
