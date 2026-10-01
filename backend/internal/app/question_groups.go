package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/CoYumeLabs/YuAction/backend/internal/storage"
)

type questionGroup struct {
	QuestionIDs []string `json:"questionIds"`
}
type questionGrouping struct {
	QuestionIDs []string        `json:"questionIds"`
	Groups      []questionGroup `json:"groups"`
	Fingerprint string          `json:"fingerprint"`
	CreatedAt   time.Time       `json:"createdAt"`
	Stale       bool            `json:"stale"`
}

// Only question identity/content affects grouping. Captions, transcription,
// answers, display state and other room revisions must not invalidate it.
func questionFingerprint(questions []Question) string {
	type input struct{ ID, Content, SegmentID, QuotedText string }
	items := make([]input, 0, len(questions))
	for _, q := range questions {
		items = append(items, input{q.ID, q.Content, q.SegmentID, q.QuotedText})
	}
	data, _ := json.Marshal(items)
	return digest(string(data))
}

// This is a manual, local lexical grouping aid, never an AI or paid job. Store
// its snapshot as a separate host-only settings item (no schema migration).
func (s *Server) questionGroups(w http.ResponseWriter, r *http.Request) {
	room, err := s.privateHost(r)
	if err != nil {
		writeError(w, err)
		return
	}
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	room, _, err = s.read(r.Context(), room.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	rec, err := s.store.GetPrivate(r.Context(), room.Code, "settings", "question-groups")
	var grouping *questionGrouping
	if errors.Is(err, storage.ErrNotFound) {
		rec = storage.PrivateRecord{Code: room.Code, Kind: "settings", ID: "question-groups"}
	} else if err != nil {
		writeError(w, err)
		return
	} else if err = json.Unmarshal(rec.Data, &grouping); err != nil {
		writeError(w, err)
		return
	}
	if r.Method == "POST" {
		// Retries after a lost response return the same saved snapshot. There is
		// no background worker or automatic regrouping on new questions.
		fingerprint := questionFingerprint(room.Questions)
		if grouping == nil || grouping.Fingerprint != fingerprint {
			next := groupQuestions(room.Questions)
			if err = s.savePrivate(r.Context(), rec, next); err != nil {
				writeError(w, err)
				return
			}
			grouping = &next
		}
	}
	if grouping != nil {
		grouping.Stale = grouping.Fingerprint != questionFingerprint(room.Questions)
	}
	respond(w, 200, map[string]any{"grouping": grouping})
}

type groupText struct {
	normalized string
	tokens     map[string]bool
	quote      string
}

func questionGroupText(q Question) groupText {
	// Different caption references may make identical short questions mean
	// different things. Be conservative and keep those references separate.
	v := groupText{tokens: map[string]bool{}, quote: q.SegmentID + "\n" + q.QuotedText}
	words := strings.FieldsFunc(strings.ToLower(q.Content), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	v.normalized = strings.Join(words, " ")
	for _, word := range words {
		var latin strings.Builder
		var previousHan rune
		flush := func() {
			if latin.Len() > 0 {
				v.tokens[latin.String()] = true
				latin.Reset()
			}
		}
		for _, r := range word {
			if unicode.Is(unicode.Han, r) {
				flush()
				if previousHan != 0 {
					v.tokens[string([]rune{previousHan, r})] = true
				}
				previousHan = r
			} else {
				previousHan = 0
				latin.WriteRune(r)
			}
		}
		flush()
	}
	return v
}

func similarGroupText(a, b groupText) bool {
	if a.quote != b.quote || a.normalized == "" || b.normalized == "" {
		return false
	}
	if a.normalized == b.normalized {
		return true
	}
	if len(a.tokens) < 3 || len(b.tokens) < 3 {
		return false
	}
	common := 0
	for token := range a.tokens {
		if b.tokens[token] {
			common++
		}
	}
	return float64(common)/float64(len(a.tokens)+len(b.tokens)-common) >= 0.6
}

func groupQuestions(questions []Question) questionGrouping {
	result := questionGrouping{QuestionIDs: []string{}, Groups: []questionGroup{}, Fingerprint: questionFingerprint(questions), CreatedAt: time.Now().UTC()}
	texts := make([]groupText, len(questions))
	for i, q := range questions {
		result.QuestionIDs = append(result.QuestionIDs, q.ID)
		texts[i] = questionGroupText(q)
	}
	buckets := [][]int{}
	for i := range questions {
		placed := false
		for j, bucket := range buckets {
			matches := true
			for _, member := range bucket {
				if !similarGroupText(texts[i], texts[member]) {
					matches = false
					break
				}
			}
			if matches {
				buckets[j] = append(buckets[j], i)
				placed = true
				break
			}
		}
		if !placed {
			buckets = append(buckets, []int{i})
		}
	}
	for _, bucket := range buckets {
		if len(bucket) < 2 {
			continue
		}
		group := questionGroup{QuestionIDs: []string{}}
		for _, i := range bucket {
			group.QuestionIDs = append(group.QuestionIDs, questions[i].ID)
		}
		result.Groups = append(result.Groups, group)
	}
	return result
}
