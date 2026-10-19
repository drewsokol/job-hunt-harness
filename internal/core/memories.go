package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
)

const (
	maxMemoryLen   = 500
	maxEvidenceLen = 500
	maxReasonLen   = 300
)

// RememberInput is a memory an agent wants to save.
type RememberInput struct {
	Kind, Content, Basis, Evidence string
	// EvidenceJobID is the job the memory was inferred from, when it came
	// from looking at a listing. Such a memory must be inferred.
	EvidenceJobID int64
	Supersedes    int64
}

// Remembered is a saved memory and the existing ones it may repeat or
// contradict, for the agent to merge or ask about.
type Remembered struct {
	ID      int64
	Related []store.Memory
}

// Remember checks and saves a memory.
func Remember(ctx context.Context, s *store.Store, by store.Actor, in RememberInput) (Remembered, error) {
	in.Content, in.Evidence = strings.TrimSpace(in.Content), strings.TrimSpace(in.Evidence)
	var problems []string
	problems = append(problems, checkMemoryFields(&in.Content, &in.Kind, &in.Basis)...)
	if utf8.RuneCountInString(in.Evidence) > maxEvidenceLen {
		problems = append(problems, fmt.Sprintf("evidence: keep it under %d characters", maxEvidenceLen))
	}
	if in.EvidenceJobID != 0 && in.Basis != "inferred" {
		problems = append(problems, "basis: a memory drawn from a job listing must be inferred, never user_said")
	}
	if len(problems) > 0 {
		return Remembered{}, &InputError{Problems: problems}
	}
	if in.EvidenceJobID != 0 {
		if _, err := s.Job(ctx, in.EvidenceJobID); err != nil {
			return Remembered{}, inputIfNotFound(err, fmt.Sprintf("evidence_job_id: no job %d", in.EvidenceJobID))
		}
	}
	active, err := s.Memories(ctx, store.MemoryQuery{})
	if err != nil {
		return Remembered{}, err
	}
	id, err := s.Remember(ctx, by, store.NewMemory{
		Kind: in.Kind, Content: in.Content, Basis: in.Basis, Evidence: in.Evidence,
		EvidenceJobID: in.EvidenceJobID, Supersedes: in.Supersedes,
	})
	if err != nil {
		return Remembered{}, inputIfNotFound(err, fmt.Sprintf("supersedes: no active memory %d", in.Supersedes))
	}
	var related []store.Memory
	for _, m := range active {
		if m.ID != in.Supersedes && similar(m.Content, in.Content) {
			related = append(related, m)
		}
	}
	return Remembered{ID: id, Related: related}, nil
}

// MemoryEdit is a change to a memory; nil fields are left as they are.
type MemoryEdit struct {
	Content, Kind, Basis *string
}

// UpdateMemory checks and applies an edit.
func UpdateMemory(ctx context.Context, s *store.Store, by store.Actor, id int64, e MemoryEdit) error {
	m, err := s.GetMemory(ctx, id)
	if err != nil {
		return inputIfNotFound(err, fmt.Sprintf("id: no memory %d", id))
	}
	content, kind, basis := m.Content, m.Kind, m.Basis
	if e.Content != nil {
		content = strings.TrimSpace(*e.Content)
	}
	if e.Kind != nil {
		kind = *e.Kind
	}
	if e.Basis != nil {
		basis = *e.Basis
	}
	problems := checkMemoryFields(&content, &kind, &basis)
	if m.EvidenceJobID != 0 && basis != "inferred" {
		problems = append(problems, "basis: this memory was drawn from a job listing, so it stays inferred")
	}
	if len(problems) > 0 {
		return &InputError{Problems: problems}
	}
	return s.UpdateMemory(ctx, by, id, store.MemoryChange{Content: &content, Kind: &kind, Basis: &basis})
}

// Forget archives a memory with the reason.
func Forget(ctx context.Context, s *store.Store, by store.Actor, id int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxReasonLen {
		return &InputError{Problems: []string{fmt.Sprintf("reason: say why in a sentence (1 to %d characters)", maxReasonLen)}}
	}
	return inputIfNotFound(s.ForgetMemory(ctx, by, id, reason), fmt.Sprintf("id: no memory %d", id))
}

func checkMemoryFields(content, kind, basis *string) []string {
	var problems []string
	if n := utf8.RuneCountInString(*content); n == 0 || n > maxMemoryLen {
		problems = append(problems, fmt.Sprintf("content: one short statement, 1 to %d characters", maxMemoryLen))
	}
	if !slices.Contains(store.MemoryKinds, *kind) {
		problems = append(problems, fmt.Sprintf("kind: %q must be one of %s", *kind, strings.Join(store.MemoryKinds, ", ")))
	}
	if !slices.Contains(store.MemoryBases, *basis) {
		problems = append(problems, fmt.Sprintf("basis: %q must be user_said (the user told you) or inferred (your own conclusion)", *basis))
	}
	return problems
}

// similar reports whether two memories share enough words to be worth a
// second look: at least half of the shorter one's significant words.
func similar(a, b string) bool {
	wa, wb := significantWords(a), significantWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return shared*2 >= min(len(wa), len(wb))
}

func significantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) > 3 {
			out[w] = true
		}
	}
	return out
}

// inputIfNotFound turns a not-found error into an InputError with msg, so
// the agent sees which argument was wrong.
func inputIfNotFound(err error, msg string) error {
	if errors.Is(err, store.ErrNotFound) {
		return &InputError{Problems: []string{msg}}
	}
	return err
}
