package store

import "testing"

func TestDoubaoProviderAndConversationRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultState()
	state.Settings.Providers = append(state.Settings.Providers, Provider{ID: "doubao", Kind: ProviderKindDoubao, Name: "豆包", Host: DoubaoHost})
	state.Settings.ActiveProviderID = "doubao"
	state.Conversations = []Conversation{{ID: "local", ProviderID: "doubao", WebKind: ProviderKindDoubao, WebURL: DoubaoChatURL + "/38447877386550786", Title: "自定义标题", TitleManual: true}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	chat := got.Conversations[0]
	if chat.WebURL != state.Conversations[0].WebURL || chat.Title != "自定义标题" || !chat.TitleManual || got.Settings.ActiveProviderID != "doubao" {
		t.Fatalf("web conversation lost: %#v", got)
	}
	// Removing the configuration must not turn saved web chats into API chats.
	got.Settings.Providers = got.Settings.Providers[:1]
	if err := s.Save(got); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || got.Conversations[0].WebKind != ProviderKindDoubao {
		t.Fatalf("orphaned web conversation lost: %v, %#v", err, got)
	}
}

func TestDoubaoURLBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://www.doubao.com/chat/38447877386550786",
		"https://www.doubao.com/chat/38447877386550786?source=chat#latest",
	} {
		got, ok := DoubaoConversationURL(raw)
		if !ok || got != DoubaoChatURL+"/38447877386550786" {
			t.Fatalf("valid route %q rejected: %q", raw, got)
		}
	}
	for _, raw := range []string{
		"http://www.doubao.com/chat/123", "https://www.doubao.com.evil.test/chat/123",
		"https://www.doubao.com@evil.test/chat/123", "https://evil.test@www.doubao.com/chat/123",
		"https://www.doubao.com:443/chat/123", "javascript:alert(1)", "file:///chat/123",
		"https://www.doubao.com/chat", "https://www.doubao.com/chat/", "https://www.doubao.com/chat/123/456",
		"https://www.doubao.com/chat/%31%32%33", "https://www.doubao.com/chat/../123",
		"https://www.doubao.com/chat/1e3", "https://www.doubao.com/security/doubao-region-ban",
	} {
		if got, ok := DoubaoConversationURL(raw); ok {
			t.Errorf("unsafe/non-conversation route accepted: %q -> %q", raw, got)
		}
	}
}

func TestDoubaoInvalidStateDoesNotReplaceSavedSnapshot(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultState()
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	state.Conversations = []Conversation{{ID: "bad", WebKind: ProviderKindDoubao, WebURL: "https://evil.test/chat/123"}}
	if err := s.Save(state); err == nil {
		t.Fatal("invalid web URL saved")
	}
	got, err := s.Load()
	if err != nil || len(got.Conversations) != 0 {
		t.Fatalf("previous snapshot not preserved: %v, %#v", err, got)
	}
}
