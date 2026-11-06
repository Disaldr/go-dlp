package youtube

import "testing"

// m — краткий конструктор map[string]any для синтетических деревьев JSON.
type m = map[string]any

func TestFindContinuation(t *testing.T) {
	tests := []struct {
		name    string
		data    any
		wantTok string
		wantAPI string
	}{
		{
			name: "viewmodel in video list (new playlist layout)",
			data: m{"contents": []any{
				m{"lockupViewModel": m{"contentId": "v1"}},
				m{"continuationItemViewModel": m{"continuationCommand": m{
					"innertubeCommand": m{
						"commandMetadata":     m{"webCommandMetadata": m{"apiUrl": "/youtubei/v1/browse"}},
						"continuationCommand": m{"token": "TOK_VM"},
					},
				}}},
			}},
			wantTok: "TOK_VM",
			wantAPI: "/youtubei/v1/browse",
		},
		{
			name: "classic renderer in video list",
			data: m{"items": []any{
				m{"playlistVideoRenderer": m{"videoId": "a"}},
				m{"continuationItemRenderer": m{"continuationEndpoint": m{
					"continuationCommand": m{"token": "TOK_R"},
				}}},
			}},
			wantTok: "TOK_R",
			wantAPI: "/youtubei/v1/browse",
		},
		{
			name: "search continuation uses search api",
			data: m{"items": []any{
				m{"videoRenderer": m{"videoId": "a"}},
				m{"continuationItemRenderer": m{"continuationEndpoint": m{
					"continuationCommand": m{"token": "TOK_S", "request": "CONTINUATION_REQUEST_TYPE_SEARCH"},
				}}},
			}},
			wantTok: "TOK_S",
			wantAPI: "/youtubei/v1/search",
		},
		{
			name: "comments continuation ignored (not a video-list sibling)",
			data: m{
				"comments": []any{
					m{"commentThreadRenderer": m{}},
					m{"continuationItemRenderer": m{"continuationEndpoint": m{
						"continuationCommand": m{"token": "COMMENTS"},
					}}},
				},
				"videos": []any{
					m{"playlistVideoRenderer": m{"videoId": "a"}},
				},
			},
			wantTok: "", // у списка видео continuation нет, комментарии не берём
		},
		{
			name: "no continuation at all",
			data: m{"items": []any{
				m{"playlistVideoRenderer": m{"videoId": "a"}},
			}},
			wantTok: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findContinuation(tt.data)
			if tt.wantTok == "" {
				if got != nil {
					t.Fatalf("expected no continuation, got token %q (api %q)", got.token, got.apiURL)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected continuation token %q, got nil", tt.wantTok)
			}
			if got.token != tt.wantTok {
				t.Errorf("token = %q, want %q", got.token, tt.wantTok)
			}
			if got.apiURL != tt.wantAPI {
				t.Errorf("apiURL = %q, want %q", got.apiURL, tt.wantAPI)
			}
		})
	}
}
