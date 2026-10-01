package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/CoYumeLabs/YuAction/backend/internal/storage"
)

func readGrouping(t *testing.T, body []byte) *questionGrouping {
	t.Helper()
	var v struct {
		Grouping *questionGrouping `json:"grouping"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v.Grouping
}

func TestManualQuestionGroupingSnapshotIsolationAndRetry(t *testing.T) {
	store := storage.NewMemory()
	s := New(store, Config{Demo: true})
	h := s.Handler()
	a, b := createRoom(t, h, "Grouping A"), createRoom(t, h, "Grouping B")
	base := "/api/rooms/" + a.Room.Code
	path := base + "/assistant/question-groups"
	ids := []string{addQuestion(t, h, a.Room.Code, "How does design thinking help users?"), addQuestion(t, h, a.Room.Code, "How does design thinking help users today?"), addQuestion(t, h, a.Room.Code, "An unrelated biology question")}
	addQuestion(t, h, b.Room.Code, "How does design thinking help users?")
	request(t, h, "GET", path, "", nil, 403)
	request(t, h, "POST", path, b.Key, nil, 403)
	if readGrouping(t, request(t, h, "GET", path, a.Key, nil, 200)) != nil {
		t.Fatal("grouped automatically")
	}
	before := readRoom(t, h, a.Room.Code)
	grouping := readGrouping(t, request(t, h, "POST", path, a.Key, nil, 200))
	if !reflect.DeepEqual(grouping.QuestionIDs, ids) || len(grouping.Groups) != 1 || !reflect.DeepEqual(grouping.Groups[0].QuestionIDs, ids[:2]) {
		t.Fatalf("unexpected grouping: %+v", grouping)
	}
	if after := readRoom(t, h, a.Room.Code); !reflect.DeepEqual(before, after) {
		t.Fatal("grouping changed public questions/state/revision")
	}
	rec, _ := store.GetPrivate(context.Background(), a.Room.Code, "settings", "question-groups")
	// A lost response and repeated clicks do not produce duplicate writes.
	for i := 0; i < 3; i++ {
		request(t, h, "POST", path, a.Key, nil, 200)
	}
	next, _ := store.GetPrivate(context.Background(), a.Room.Code, "settings", "question-groups")
	if rec.Revision != next.Revision || !bytes.Equal(rec.Data, next.Data) {
		t.Fatal("repeat mutated grouping")
	}
	request(t, h, "POST", base+"/demo-segments", a.Key, map[string]string{"id": "caption", "text": "Unrelated subtitle"}, 200)
	request(t, h, "PATCH", base+"/questions/"+ids[0], a.Key, map[string]string{"status": "showing"}, 200)
	request(t, h, "PATCH", base+"/questions/"+ids[1], a.Key, map[string]string{"status": "answered"}, 200)
	if readGrouping(t, request(t, h, "GET", path, a.Key, nil, 200)).Stale {
		t.Fatal("caption or answered/display state invalidated grouping")
	}
	newID := addQuestion(t, h, a.Room.Code, "How does design thinking help users?")
	old := readGrouping(t, request(t, h, "GET", path, a.Key, nil, 200))
	if !old.Stale || !reflect.DeepEqual(old.QuestionIDs, ids) {
		t.Fatal("new question caused automatic regroup")
	}
	request(t, h, "DELETE", base+"/questions/"+ids[0], a.Key, nil, 200)
	grouping = readGrouping(t, request(t, h, "POST", path, a.Key, nil, 200))
	if grouping.Stale || !reflect.DeepEqual(grouping.QuestionIDs, []string{ids[1], ids[2], newID}) {
		t.Fatalf("removed/new IDs: %+v", grouping)
	}
	room := readRoom(t, h, a.Room.Code)
	if room.Questions[0].Status != "answered" {
		t.Fatal("answered state lost")
	}
	if readGrouping(t, request(t, h, "GET", "/api/rooms/"+b.Room.Code+"/assistant/question-groups", b.Key, nil, 200)) != nil {
		t.Fatal("cross-room grouping leaked")
	}
	public := request(t, h, "GET", base, "", nil, 200)
	if bytes.Contains(public, []byte("fingerprint")) || bytes.Contains(public, []byte("questionIds")) {
		t.Fatal("grouping leaked into public snapshot")
	}
	// There is no pending background task to strand on a process restart.
	restarted := New(store, Config{Demo: true})
	recovered := readGrouping(t, request(t, restarted.Handler(), "GET", path, a.Key, nil, 200))
	if !reflect.DeepEqual(grouping, recovered) {
		t.Fatal("saved grouping not recovered")
	}
}

func TestLexicalGroupingIsConservativeAndPreservesAllIDs(t *testing.T) {
	questions := []Question{
		{ID: "1", Content: "设计思维如何帮助理解用户需求？"},
		{ID: "2", Content: "设计思维如何帮助理解用户需求呢？", Status: "answered"},
		{ID: "3", Content: "你好"}, {ID: "4", Content: "你好！"},
		{ID: "5", Content: "你好", SegmentID: "different-caption", QuotedText: "Different context"},
		{ID: "6", Content: "Why?"}, {ID: "7", Content: "Why not?"},
		{ID: "8", Content: "How does design thinking help users?"},
		{ID: "9", Content: "HOW DOES DESIGN THINKING HELP USERS!"},
	}
	v := groupQuestions(questions)
	if len(v.Groups) != 3 {
		t.Fatalf("unexpected groups: %+v", v.Groups)
	}
	seen := map[string]bool{}
	for _, g := range v.Groups {
		for _, id := range g.QuestionIDs {
			if seen[id] {
				t.Fatal("duplicate placement")
			}
			seen[id] = true
		}
	}
	if seen["5"] || seen["6"] || seen["7"] || len(v.QuestionIDs) != len(questions) {
		t.Fatal("unrelated/short contexts grouped or snapshot lost IDs")
	}
}

type failingGroupingStore struct {
	storage.Store
	failSave bool
}

func (s *failingGroupingStore) SavePrivate(ctx context.Context, rev int64, rec storage.PrivateRecord) error {
	if s.failSave && rec.ID == "question-groups" {
		return errors.New("fixture storage unavailable")
	}
	return s.Store.SavePrivate(ctx, rev, rec)
}

func TestGroupingFailureLeavesQuestionsAndSavedSnapshot(t *testing.T) {
	store := &failingGroupingStore{Store: storage.NewMemory()}
	s := New(store, Config{Demo: true})
	h := s.Handler()
	a := createRoom(t, h, "Failure")
	path := "/api/rooms/" + a.Room.Code + "/assistant/question-groups"
	addQuestion(t, h, a.Room.Code, "Same question")
	addQuestion(t, h, a.Room.Code, "Same question")
	saved := readGrouping(t, request(t, h, "POST", path, a.Key, nil, 200))
	addQuestion(t, h, a.Room.Code, "A new question")
	before := readRoom(t, h, a.Room.Code)
	store.failSave = true
	request(t, h, "POST", path, a.Key, nil, 500)
	got := readGrouping(t, request(t, h, "GET", path, a.Key, nil, 200))
	if !got.Stale || !reflect.DeepEqual(got.Groups, saved.Groups) || !reflect.DeepEqual(before, readRoom(t, h, a.Room.Code)) {
		t.Fatal("failure lost questions or saved groups")
	}
	store.failSave = false
	request(t, h, "POST", path, a.Key, nil, 200)
}

func TestYufoloGroupingRequiresRoomOwnerAndMakesNoAICalls(t *testing.T) {
	f := newFakeYufolo(t)
	s := New(storage.NewMemory(), Config{YufoloURL: f.server.URL})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	teacher := integrationClient(t, server.URL, "teacher@example.com")
	other := integrationClient(t, server.URL, "other@example.com")
	room := integratedRoom(t, teacher, server.URL)
	path := server.URL + "/api/rooms/" + room.Room.Code + "/assistant/question-groups"
	addQuestion(t, s.Handler(), room.Room.Code, "Same question")
	addQuestion(t, s.Handler(), room.Room.Code, "Same question")
	httpJSON(t, http.DefaultClient, path, "POST", nil, 401)
	httpJSON(t, other, path, "POST", nil, 403)
	httpJSON(t, teacher, path, "POST", nil, 200)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ai.asks) != 0 || len(f.ai.projects) != 0 {
		t.Fatal("local grouping called paid AI/project APIs")
	}
}
