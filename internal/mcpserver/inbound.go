package mcpserver

import "github.com/drewsokol/job-hunt-harness/internal/store"

// payOut is pay a person mentioned on a thread, apart from any listing's.
type payOut struct {
	Min      float64 `json:"min,omitempty"`
	Max      float64 `json:"max,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Period   string  `json:"period,omitempty"`
	Note     string  `json:"note,omitempty"`
}

func pay(p store.OutreachPay) *payOut {
	if p.IsZero() {
		return nil
	}
	return &payOut{p.Min, p.Max, p.Currency, p.Period, p.Note}
}
