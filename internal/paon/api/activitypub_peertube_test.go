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
			name: "Undo Create CacheFile",
			body: `{
				"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1",{"RsaSignature2017":"https://w3id.org/security#RsaSignature2017"},{"pt":"https://joinpeertube.org/ns#","sc":"http://schema.org/","expires":"sc:expires","CacheFile":"pt:CacheFile","size":{"@type":"sc:Number","@id":"pt:size"},"fps":{"@type":"sc:Number","@id":"pt:fps"}}],
				"to":["https://video.blender.org/accounts/blender"],"cc":[],
				"type":"Undo",
				"id":"https://video.chasmcity.net/redundancy/streaming-playlists/hls/1e245cf5-84f1-4ed5-aae1-3c1fb7d19020/undo",
				"actor":"https://video.chasmcity.net/accounts/peertube",
				"object":{
					"to":["https://www.w3.org/ns/activitystreams#Public"],"cc":["https://video.chasmcity.net/accounts/peertube/followers"],
					"type":"Create",
					"id":"https://video.chasmcity.net/redundancy/streaming-playlists/hls/1e245cf5-84f1-4ed5-aae1-3c1fb7d19020/activity",
					"actor":"https://video.chasmcity.net/accounts/peertube",
					"object":{
						"to":["https://www.w3.org/ns/activitystreams#Public"],"cc":["https://video.chasmcity.net/accounts/peertube/followers"],
						"id":"https://video.chasmcity.net/redundancy/streaming-playlists/hls/1e245cf5-84f1-4ed5-aae1-3c1fb7d19020",
						"type":"CacheFile",
						"object":"https://video.blender.org/videos/watch/1e245cf5-84f1-4ed5-aae1-3c1fb7d19020",
						"expires":"2026-09-18T15:33:34.899Z",
						"url":{"type":"Link","mediaType":"application/x-mpegURL","href":"https://video.chasmcity.net/static/redundancy/hls/1e245cf5-84f1-4ed5-aae1-3c1fb7d19020"}
					}
				},
				"signature":{"type":"RsaSignature2017","creator":"https://video.chasmcity.net/accounts/peertube","created":"2026-09-17T18:33:21.313Z","signatureValue":"test-signature"}
			}`,
			objectType: "Create",
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

func TestActivityPubPeerTubeAdditionalNotifications(t *testing.T) {
	const actorURI = "https://remote.example/accounts/peertube"
	actor := &models.Account{ID: 42, URI: actorURI, Domain: sql.NullString{String: "remote.example", Valid: true}}
	server := &Server{db: &gorm.DB{}}
	for _, test := range []struct {
		activityType string
		objectType   string
		nestedType   string
	}{
		{activityType: "Update", objectType: "CacheFile"},
		{activityType: "Undo", objectType: "Create", nestedType: "CacheFile"},
		{activityType: "Dislike"},
		{activityType: "Undo", objectType: "Dislike"},
		{activityType: "Create", objectType: "Playlist"},
		{activityType: "Update", objectType: "Playlist"},
		{activityType: "Create", objectType: "WatchAction"},
	} {
		t.Run(test.activityType+"/"+test.objectType, func(t *testing.T) {
			for _, signed := range []bool{false, true} {
				raw := map[string]any{
					"@context": []any{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1", map[string]any{
						"RsaSignature2017": "https://w3id.org/security#RsaSignature2017",
						"pt":               "https://joinpeertube.org/ns#", "sc": "http://schema.org/",
						"CacheFile": "pt:CacheFile", "Playlist": "pt:Playlist", "WatchAction": "sc:WatchAction",
					}},
					"id": actorURI + "/activities/1", "actor": actorURI, "type": test.activityType,
					"object": actorURI + "/videos/1",
				}
				if test.objectType != "" {
					object := map[string]any{"id": actorURI + "/objects/1", "type": test.objectType, "actor": actorURI}
					if test.nestedType != "" {
						object["object"] = map[string]any{"id": actorURI + "/cache/1", "type": test.nestedType}
					} else {
						object["object"] = actorURI + "/videos/1"
					}
					raw["object"] = object
				}
				if signed {
					raw["signature"] = map[string]any{"type": "RsaSignature2017", "creator": actorURI, "created": "2026-09-19T00:00:00Z", "signatureValue": "test-signature"}
				}
				body, err := json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, actor, nil, 0); err != nil {
					t.Errorf("signature=%t: notification processing: %v", signed, err)
				}
				if test.activityType == "Undo" {
					raw["object"].(map[string]any)["actor"] = "https://other.example/accounts/peertube"
					body, err = json.Marshal(raw)
					if err != nil {
						t.Fatal(err)
					}
					if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, actor, nil, 0); !errors.Is(err, errActivityPubEventNotApplied) {
						t.Errorf("signature=%t: mismatched embedded actor error = %v", signed, err)
					}
				}
			}
		})
	}
}

func TestActivityPubPeerTubeUndoCreateOnlyIgnoresCacheFile(t *testing.T) {
	const actorURI = "https://remote.example/accounts/peertube"
	actor := &models.Account{ID: 42, URI: actorURI, Domain: sql.NullString{String: "remote.example", Valid: true}}
	server := &Server{db: &gorm.DB{}}
	for _, object := range []any{
		nil,
		actorURI + "/cache/1",
		map[string]any{"id": actorURI + "/cache/1"},
		map[string]any{"id": actorURI + "/cache/1", "type": "Note"},
		map[string]any{"id": actorURI + "/cache/1", "type": "Video"},
		map[string]any{"id": actorURI + "/cache/1", "type": "UnknownObject"},
		map[string]any{"id": actorURI + "/cache/1", "type": "https://unrelated.example/ns#CacheFile"},
		map[string]any{"id": actorURI + "/cache/1", "type": []any{"CacheFile", "Note"}},
	} {
		body, err := json.Marshal(map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id":       actorURI + "/undo/1", "actor": actorURI, "type": "Undo",
			"object": map[string]any{"id": actorURI + "/create/1", "actor": actorURI, "type": "Create", "object": object},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, actor, nil, 0); !errors.Is(err, errActivityPubEventNotApplied) {
			t.Errorf("nested object=%v: processing error = %v", object, err)
		}
	}
}
