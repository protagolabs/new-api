package ali

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samber/lo"
)

// A value-typed bool with omitempty drops `false` from the payload entirely,
// so the vendor never sees it and applies its own default -- which for
// HappyHorse means a watermark. Customers reported watermarked videos while we
// believed we were sending watermark:false. These fields must stay pointers.
func TestBoolParamsSurviveFalse(t *testing.T) {
	p := AliVideoParameters{
		PromptExtend: lo.ToPtr(false),
		Watermark:    lo.ToPtr(false),
		Audio:        lo.ToPtr(false),
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"prompt_extend":false`, `"watermark":false`, `"audio":false`} {
		if !strings.Contains(got, want) {
			t.Errorf("payload %s is missing %s -- a false value was silently dropped", got, want)
		}
	}
}

// The default request must actively disable the watermark rather than leaving
// the field out.
func TestDefaultRequestDisablesWatermark(t *testing.T) {
	p := AliVideoParameters{
		PromptExtend: lo.ToPtr(true),
		Watermark:    lo.ToPtr(false),
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"watermark":false`) {
		t.Errorf("default parameters must send watermark:false, got %s", string(b))
	}
}
