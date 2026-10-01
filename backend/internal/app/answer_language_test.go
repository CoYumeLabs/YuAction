package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/CoYumeLabs/YuAction/backend/internal/storage"
)

func TestAnswerLanguageDefaultsPromptPrecedenceAndPersistence(t *testing.T) {
	store := storage.NewMemory()
	s := New(store, Config{Demo: true, AI: AIConfig{GenericPrompt: "Environment generic", KBPrompt: "Environment KB"}})
	h := s.Handler()
	a, b := createRoom(t, h, "Language A"), createRoom(t, h, "Language B")
	base := "/api/rooms/" + a.Room.Code + "/assistant"
	// Old saved settings have no language field and retain old prompt precedence.
	old := storage.PrivateRecord{Code: a.Room.Code, Kind: "settings", ID: "default", Data: []byte(`{"topK":3,"genericPrompt":"Room generic","kbPrompt":""}`)}
	if err := store.SavePrivate(context.Background(), 0, old); err != nil {
		t.Fatal(err)
	}
	settings, err := s.assistantSettings(context.Background(), a.Room.Code)
	if err != nil || settings.AnswerPreset != "" || settings.GenericPrompt != "Room generic" || settings.KBPrompt != "Environment KB" {
		t.Fatalf("legacy settings: %+v %v", settings, err)
	}
	request(t, h, "PUT", base+"/settings", b.Key, settings, 403)
	request(t, h, "GET", base, "", nil, 403)
	settings.AnswerPreset = answerPresetChinese
	request(t, h, "PUT", base+"/settings", a.Key, settings, 200)
	invalid := settings
	invalid.AnswerPreset = "english"
	request(t, h, "PUT", base+"/settings", a.Key, invalid, 400)
	restarted := New(store, s.cfg)
	settings, err = restarted.assistantSettings(context.Background(), a.Room.Code)
	if err != nil || settings.AnswerPreset != answerPresetChinese || settings.GenericPrompt != "Room generic" {
		t.Fatal("language or custom prompt not persisted")
	}
	other, _ := restarted.assistantSettings(context.Background(), b.Room.Code)
	if other.AnswerPreset != "" {
		t.Fatal("room setting leaked")
	}
	if public := request(t, h, "GET", "/api/rooms/"+a.Room.Code, "", nil, 200); bytes.Contains(public, []byte("answerPreset")) {
		t.Fatal("language settings leaked publicly")
	}
	for _, prompt := range []string{defaultGenericPrompt, defaultKBPrompt, "Answer in English; translate. %s"} {
		if answerPrompt(prompt, "") != prompt {
			t.Fatal("old answer behavior changed")
		}
		chinese := answerPrompt(prompt, answerPresetChinese)
		if !strings.HasSuffix(chinese, chineseOnlyRule) || strings.Contains(chinese, defaultGenericPrompt) || strings.Contains(chinese, defaultKBPrompt) {
			t.Fatalf("language precedence: %s", chinese)
		}
		if bilingual := answerPrompt(prompt, answerPresetBilingual); !strings.HasPrefix(bilingual, prompt) || !strings.HasSuffix(bilingual, bilingualRule) {
			t.Fatal("bilingual preset lost teacher prompt or language precedence")
		}
	}
}

func TestStandaloneChineseAnswerRoutesAndPreservesOldDraft(t *testing.T) {
	for _, mode := range []string{"built-in", "environment", "room"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			prompts := []string{}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/embeddings" {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 0}}}})
					return
				}
				var in struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&in)
				mu.Lock()
				prompts = append(prompts, in.Messages[0].Content)
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "中文 [1] / English example with API"}}}})
			}))
			defer provider.Close()
			cfg := AIConfig{Key: "fake", Model: "fixture", ChatURL: provider.URL + "/chat", EmbeddingKey: "fake", EmbeddingModel: "fixture", EmbeddingURL: provider.URL + "/embeddings"}
			if mode == "environment" {
				cfg.GenericPrompt = "Environment: English only"
				cfg.KBPrompt = "Environment: bilingual evidence %s"
			}
			s := New(storage.NewMemory(), Config{Demo: true, AI: cfg})
			h := s.Handler()
			room := createRoom(t, h, mode)
			base := "/api/rooms/" + room.Room.Code
			doc := knowledgeDocument{ID: "doc", Name: "lecture.txt", Status: "ready", Version: cfg.embeddingVersion(), Chunks: []knowledgeChunk{{Text: "FIXTURE EVIDENCE", Vector: []float32{1, 0}}}}
			if err := s.savePrivate(context.Background(), storage.PrivateRecord{Code: room.Room.Code, Kind: "document", ID: doc.ID}, doc); err != nil {
				t.Fatal(err)
			}
			settings, _ := s.assistantSettings(context.Background(), room.Room.Code)
			if mode == "room" {
				settings.GenericPrompt = "Room: English only"
				settings.KBPrompt = "Room: English translation %s"
			}
			request(t, h, "PUT", base+"/assistant/settings", room.Key, settings, 200)
			id := addQuestion(t, h, room.Room.Code, "Fixture question")
			request(t, h, "POST", base+"/assistant/answers/"+id, room.Key, nil, 202)
			old := waitDraft(t, s, room.Room.Code, id)
			if old.AnswerPreset != "" || old.Generic.Status != "ready" || len(old.Knowledge.Sources) != 1 {
				t.Fatalf("old draft: %+v", old)
			}
			settings.AnswerPreset = answerPresetChinese
			request(t, h, "PUT", base+"/assistant/settings", room.Key, settings, 200)
			stale := waitDraft(t, s, room.Room.Code, id)
			if stale.AnswerPreset != "" || stale.Generic.Status != "outdated" || stale.Generic.Text != old.Generic.Text || stale.Knowledge.Text != old.Knowledge.Text || len(stale.Knowledge.Sources) != 1 || stale.Knowledge.Sources[0] != old.Knowledge.Sources[0] {
				t.Fatalf("draft text/source/language lost: %+v", stale)
			}
			mu.Lock()
			count := len(prompts)
			mu.Unlock()
			if count != 2 {
				t.Fatal("settings update automatically called AI")
			}
			request(t, h, "POST", base+"/assistant/answers/"+id, room.Key, nil, 202)
			draft := waitDraft(t, s, room.Room.Code, id)
			if draft.AnswerPreset != answerPresetChinese || draft.Generic.Status != "ready" || draft.Knowledge.Status != "ready" {
				t.Fatalf("new draft: %+v", draft)
			}
			mu.Lock()
			if len(prompts) != 4 {
				t.Fatalf("provider calls: %d", len(prompts))
			}
			for _, prompt := range prompts[2:] {
				if !strings.HasSuffix(prompt, chineseOnlyRule) || strings.Contains(prompt, "%s") {
					t.Fatalf("routing/substitution: %s", prompt)
				}
				if mode == "built-in" && (strings.Contains(prompt, defaultGenericPrompt) || strings.Contains(prompt, defaultKBPrompt)) {
					t.Fatal("bilingual default contradicted language")
				}
				if mode == "room" && !strings.Contains(prompt, "Room:") || mode == "environment" && !strings.Contains(prompt, "Environment:") {
					t.Fatal("custom prompt precedence lost")
				}
			}
			mu.Unlock()
			settings.AnswerPreset = answerPresetBilingual
			request(t, h, "PUT", base+"/assistant/settings", room.Key, settings, 200)
			request(t, h, "POST", base+"/assistant/answers/"+id, room.Key, nil, 202)
			draft = waitDraft(t, s, room.Room.Code, id)
			if draft.AnswerPreset != answerPresetBilingual || draft.Generic.Status != "ready" || draft.Knowledge.Status != "ready" {
				t.Fatal("bilingual standalone preset not routed")
			}
			mu.Lock()
			if len(prompts) != 6 {
				t.Fatal("unexpected standalone regeneration calls")
			}
			for _, prompt := range prompts[4:] {
				if !strings.HasSuffix(prompt, bilingualRule) {
					t.Fatal("standalone bilingual rule missing")
				}
			}
			mu.Unlock()
			if room := readRoom(t, h, room.Room.Code); room.Questions[0].Status != "pending" {
				t.Fatal("AI draft treated as teacher answer")
			}
		})
	}
}

func TestYufoloChineseAnswerPromptAndContextRouting(t *testing.T) {
	f := newFakeYufolo(t)
	s := New(storage.NewMemory(), Config{YufoloURL: f.server.URL})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	teacher := integrationClient(t, server.URL, "teacher@example.com")
	room := integratedRoom(t, teacher, server.URL)
	base := server.URL + "/api/rooms/" + room.Room.Code
	httpJSON(t, teacher, base+"/transcription", "POST", map[string]string{"sourceLanguage": "cmn"}, 200)
	uploadFixture(t, teacher, base+"/assistant/documents", "", "fixture.txt", "Synthetic evidence")
	settings, _ := s.assistantSettings(context.Background(), room.Room.Code)
	settings.AnswerPreset = answerPresetChinese
	settings.GenericPrompt = "Custom English only"
	settings.KBPrompt = "Custom bilingual with %s"
	httpJSON(t, teacher, base+"/assistant/settings", "PUT", settings, 200)
	id := addQuestion(t, s.Handler(), room.Room.Code, "Synthetic question")
	httpJSON(t, teacher, base+"/assistant/answers/"+id, "POST", nil, 202)
	draft := waitDraft(t, s, room.Room.Code, id)
	if draft.AnswerPreset != answerPresetChinese || draft.Generic.Status != "ready" || draft.Knowledge.Status != "ready" {
		t.Fatalf("draft: %+v", draft)
	}
	_, link, _ := s.read(context.Background(), room.Room.Code)
	f.mu.Lock()
	if len(f.ai.asks) != 2 {
		t.Fatalf("unexpected AI calls: %d", len(f.ai.asks))
	}
	for _, ask := range f.ai.asks {
		prompt := ask["config"].(map[string]any)["prompt"].(string)
		if !strings.HasSuffix(prompt, chineseOnlyRule) || strings.Contains(prompt, "%s") || ask["stateless"] != true {
			t.Fatalf("prompt/state: %+v", ask)
		}
		if ask["project_id"] == nil {
			if ask["session_id"] != nil || !strings.Contains(prompt, settings.GenericPrompt) {
				t.Fatal("generic route has KB/history")
			}
		} else if ask["project_id"] != settings.ProjectID || ask["session_id"] != link.Link.SessionID || !strings.Contains(prompt, "Custom bilingual") {
			t.Fatal("knowledge route lost project/session/prompt")
		}
	}
	f.mu.Unlock()
	settings.AnswerPreset = answerPresetBilingual
	httpJSON(t, teacher, base+"/assistant/settings", "PUT", settings, 200)
	old := waitDraft(t, s, room.Room.Code, id)
	if old.AnswerPreset != answerPresetChinese || old.Generic.Text != draft.Generic.Text || old.Generic.Status != "outdated" || len(old.Knowledge.Sources) != len(draft.Knowledge.Sources) {
		t.Fatal("Yufolo draft/source/generation language not preserved")
	}
	f.mu.Lock()
	if len(f.ai.asks) != 2 {
		t.Fatal("language toggle made paid call")
	}
	f.mu.Unlock()
	httpJSON(t, teacher, base+"/assistant/answers/"+id, "POST", nil, 202)
	draft = waitDraft(t, s, room.Room.Code, id)
	if draft.AnswerPreset != answerPresetBilingual || draft.Generic.Status != "ready" || draft.Knowledge.Status != "ready" {
		t.Fatal("bilingual preset not routed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ai.asks) != 4 {
		t.Fatal("unexpected regeneration calls")
	}
	for _, ask := range f.ai.asks[2:] {
		if prompt := ask["config"].(map[string]any)["prompt"].(string); !strings.HasSuffix(prompt, bilingualRule) {
			t.Fatal("bilingual preset missing in generic/KB route")
		}
	}
}

func TestAnswerSettingsChangeDuringGenerationCannotBeOverwritten(t *testing.T) {
	for _, tc := range []struct{ integrated, promptChange bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		integrated := tc.integrated
		name := "standalone"
		if integrated {
			name = "yufolo"
		}
		if tc.promptChange {
			name += "-custom-prompt"
		}
		t.Run(name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var upstream http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Late English answer"}}}})
			})
			if integrated {
				f := newFakeYufolo(t)
				upstream = f.server.Config.Handler
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !integrated || r.URL.Path == "/api/rag/ask" {
					once.Do(func() { close(started) })
					<-release
				}
				upstream.ServeHTTP(w, r)
			}))
			defer provider.Close()
			defer close(release)
			cfg := Config{Demo: true, AI: AIConfig{Key: "fake", Model: "fixture", ChatURL: provider.URL}}
			if integrated {
				cfg = Config{YufoloURL: provider.URL}
			}
			s := New(storage.NewMemory(), cfg)
			h := s.Handler()
			var code, key string
			var client *http.Client
			var server *httptest.Server
			if integrated {
				server = httptest.NewServer(h)
				defer server.Close()
				client = integrationClient(t, server.URL, "teacher@example.com")
				room := integratedRoom(t, client, server.URL)
				code = room.Room.Code
			} else {
				room := createRoom(t, h, "In-flight")
				code, key = room.Room.Code, room.Key
			}
			base := "/api/rooms/" + code + "/assistant"
			call := func(path, method string, body any, status int) {
				if integrated {
					httpJSON(t, client, server.URL+path, method, body, status)
				} else {
					request(t, h, method, path, key, body, status)
				}
			}
			id := addQuestion(t, h, code, "Synthetic in-flight question")
			call(base+"/answers/"+id, "POST", nil, 202)
			<-started
			settings, _ := s.assistantSettings(context.Background(), code)
			settings.AnswerPreset = answerPresetChinese
			expectedMessage := "回答语言设置已变更"
			if tc.promptChange {
				settings.AnswerPreset = ""
				settings.GenericPrompt = "New teacher edited prompt"
				expectedMessage = "回答提示词或检索设置已变更"
			}
			call(base+"/settings", "PUT", settings, 200)
			eventually(t, func() bool { s.aiMu.Lock(); defer s.aiMu.Unlock(); return len(s.aiJobs) == 0 })
			stale := waitDraft(t, s, code, id)
			if stale.AnswerPreset != "" || stale.Generic.Status != "outdated" || stale.Generic.Text != "" || !strings.Contains(stale.Generic.Error, expectedMessage) {
				t.Fatalf("old-language worker replaced invalidation: %+v", stale)
			}
		})
	}
}
