package web

import (
	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// payJSON is pay a person mentioned on an outreach thread.
type payJSON struct {
	Min      float64 `json:"min,omitempty"`
	Max      float64 `json:"max,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Period   string  `json:"period,omitempty"`
	Note     string  `json:"note,omitempty"`
}

func payOf(p store.OutreachPay) *payJSON {
	if p.IsZero() {
		return nil
	}
	return &payJSON{p.Min, p.Max, p.Currency, p.Period, p.Note}
}

// input is the pay as core checks it; nil is no pay.
func (p *payJSON) input() core.PayInput {
	if p == nil {
		return core.PayInput{}
	}
	return core.PayInput{Min: p.Min, Max: p.Max, Currency: p.Currency, Period: p.Period, Note: p.Note}
}
