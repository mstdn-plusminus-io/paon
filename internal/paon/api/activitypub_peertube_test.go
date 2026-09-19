package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestActivityPubPeerTubeNotificationsAreAcceptedWithoutApplyingState(t *testing.T) {
	for _, test := range []struct {
		name       string
		body       string
		objectType string
	}{
		{
			name: "Create CacheFile",
			body: `{
				"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1",{"RsaSignature2017":"https://w3id.org/security#RsaSignature2017"},{"pt":"https://joinpeertube.org/ns#","sc":"http://schema.org/","expires":"sc:expires","CacheFile":"pt:CacheFile"}],
				"to":["https://video.blender.org/accounts/blender"],"cc":[],
				"type":"Create",
				"id":"https://peertube.behostings.net/redundancy/videos/d50af361-1805-4d67-92e7-20eec0c30009/1080-30/activity",
				"actor":"https://peertube.behostings.net/accounts/peertube",
				"object":{
					"to":["https://video.blender.org/accounts/blender"],"cc":[],
					"id":"https://peertube.behostings.net/redundancy/videos/d50af361-1805-4d67-92e7-20eec0c30009/1080-30",
					"type":"CacheFile",
					"object":"https://video.blender.org/videos/watch/d50af361-1805-4d67-92e7-20eec0c30009",
					"expires":"2026-09-19T12:32:16.031Z",
					"url":{"type":"Link","mediaType":"video/mp4","href":"https://peertube.behostings.net/static/redundancy/fdb8a1aa-3ac1-41a9-aadf-5610e86f4653-1080.mp4","height":1080,"size":31191786,"fps":30}
				},
				"signature":{"type":"RsaSignature2017","creator":"https://peertube.behostings.net/accounts/peertube","created":"2026-09-19T00:34:09.158Z","signatureValue":"test-signature"}
			}`,
			objectType: "CacheFile",
		},
		{
			name: "Download",
			body: `{
				"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1",{"RsaSignature2017":"https://w3id.org/security#RsaSignature2017"},{"pt":"https://joinpeertube.org/ns#","sc":"http://schema.org/","DownloadAction":"sc:DownloadAction","InteractionCounter":"sc:InteractionCounter","interactionType":"sc:interactionType","userInteractionCount":"sc:userInteractionCount"}],
				"to":["https://www.w3.org/ns/activitystreams#Public","https://video.blender.org/video-channels/blender_channel"],
				"cc":["https://video.blender.org/accounts/blender/followers"],
				"id":"https://video.blender.org/accounts/peertube/downloads/videos/56956/tkSwXt5xi62sgndan8bKz8",
				"type":"Download",
				"actor":"https://video.blender.org/accounts/peertube",
				"object":"https://video.blender.org/videos/watch/64e386c6-5160-4f98-9655-edf3508d101e",
				"signature":{"type":"RsaSignature2017","creator":"https://video.blender.org/accounts/peertube","created":"2026-09-19T02:44:50.734Z","signatureValue":"test-signature"}
			}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(test.body), &raw); err != nil {
				t.Fatal(err)
			}
			actor := &models.Account{
				ID:     106836212681004967,
				URI:    raw["actor"].(string),
				Domain: sql.NullString{String: "remote.example", Valid: true},
			}
			// No usable database or queue: these notifications must not apply
			// state or dereference the video/cache URL. The caller has already
			// verified the HTTP signature for actor, as for normal inbox jobs.
			server := &Server{db: &gorm.DB{}}
			for _, signed := range []bool{true, false} {
				if !signed {
					delete(raw, "signature")
				}
				body, err := json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := parseActivityPayload(activityPubCompactCollectionBody(body))
				if err != nil {
					t.Fatal(err)
				}
				if payload.Type != raw["type"] || payload.Object.TypeExact != test.objectType {
					t.Errorf("signature=%t: parsed type=%q object type=%q", signed, payload.Type, payload.Object.TypeExact)
				}
				if fields := activityPubLogFieldsFromBody(body); fields.Type != raw["type"] {
					t.Errorf("signature=%t: log type=%q, want %q", signed, fields.Type, raw["type"])
				}
				if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, actor, nil, 0); err != nil {
					t.Errorf("signature=%t: notification processing: %v", signed, err)
				}
				if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, nil, nil, 0); !errors.Is(err, errActivityPubEventNotApplied) {
					t.Errorf("signature=%t: missing verified actor error = %v", signed, err)
				}
			}
			// An unsigned notification from a different HTTP signer must still
			// fail authentication before reaching the no-op dispatch.
			body, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			other := *actor
			other.URI = "https://other.example/accounts/peertube"
			if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, &other, nil, 0); !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "activity actor does not match") {
				t.Errorf("mismatched verified actor error = %v", err)
			}
		})
	}
}

func TestActivityPubPeerTubeDownloadGraph(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"@context":"https://www.w3.org/ns/activitystreams",
		"@graph":[
			{"id":"https://remote.example/counters/1","type":"InteractionCounter"},
			{"id":"https://remote.example/downloads/1","type":"Download","actor":"https://remote.example/accounts/peertube","object":"https://remote.example/videos/1"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Download" || payload.Actor != "https://remote.example/accounts/peertube" {
		t.Fatalf("graph activity type=%q actor=%q", payload.Type, payload.Actor)
	}
}

func TestActivityPubPeerTubeUnknownTypesRemainUnsupported(t *testing.T) {
	server := &Server{db: &gorm.DB{}}
	actor := &models.Account{
		ID: 42, URI: "https://remote.example/accounts/peertube",
		Domain: sql.NullString{String: "remote.example", Valid: true},
	}
	for _, test := range []struct {
		activityType string
		objectType   string
	}{
		{activityType: "Travel", objectType: "CacheFile"},
		{activityType: "Create", objectType: "UnknownObject"},
		{activityType: "Create", objectType: "https://unrelated.example/ns#CacheFile"},
		{activityType: "https://joinpeertube.org/ns#Delete", objectType: "CacheFile"},
		{activityType: "pt:Delete", objectType: "CacheFile"},
	} {
		t.Run(test.activityType+"/"+test.objectType, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"@context": "https://www.w3.org/ns/activitystreams",
				"id":       actor.URI + "/activities/1", "actor": actor.URI, "type": test.activityType,
				"object": map[string]any{"id": actor.URI + "/objects/1", "type": test.objectType},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, actor, nil, 0); !errors.Is(err, errActivityPubEventNotApplied) {
				t.Fatalf("unsupported type processing error = %v", err)
			}
		})
	}
}
