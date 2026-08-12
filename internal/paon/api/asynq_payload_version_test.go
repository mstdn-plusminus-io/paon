package api

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
)

func TestAllAsynqPayloadShapesEmitCurrentVersion(t *testing.T) {
	payloads := []struct {
		name    string
		payload any
	}{
		{name: "account", payload: asynqAccountPayload{}},
		{name: "fetch reply", payload: asynqFetchReplyPayload{}},
		{name: "fetch replies", payload: asynqFetchRepliesPayload{}},
		{name: "fetch all replies", payload: asynqFetchAllRepliesPayload{}},
		{name: "thread resolve", payload: asynqThreadResolvePayload{}},
		{name: "mention resolve", payload: asynqMentionResolvePayload{}},
		{name: "notification pair", payload: asynqNotificationPairPayload{}},
		{name: "annual report", payload: asynqAnnualReportPayload{}},
		{name: "resolve account", payload: asynqResolveAccountPayload{}},
		{name: "feed insert", payload: asynqFeedInsertPayload{}},
		{name: "local notification", payload: asynqLocalNotificationPayload{}},
		{name: "notification mail", payload: asynqNotificationMailPayload{}},
		{name: "confirmation mail", payload: asynqConfirmationMailPayload{}},
		{name: "mailer delivery", payload: asynqMailerDeliveryPayload{}},
		{name: "status", payload: asynqStatusPayload{}},
		{name: "poll", payload: asynqPollPayload{}},
		{name: "scheduled status", payload: asynqScheduledStatusPayload{}},
		{name: "announcement", payload: asynqAnnouncementPayload{}},
		{name: "announcement distribution", payload: asynqAnnouncementDistributionPayload{}},
		{name: "terms of service distribution", payload: asynqTermsOfServiceDistributionPayload{}},
		{name: "account raw distribution", payload: asynqAccountRawDistributionPayload{}},
		{name: "raw distribution", payload: asynqRawDistributionPayload{}},
		{name: "featured collection", payload: asynqFeaturedCollectionPayload{}},
		{name: "featured tags", payload: asynqFeaturedTagsPayload{}},
		{name: "followers synchronization", payload: asynqFollowersSynchronizationPayload{}},
		{name: "migration", payload: asynqMigrationPayload{}},
		{name: "domain", payload: asynqDomainPayload{}},
		{name: "account domain", payload: asynqAccountDomainPayload{}},
		{name: "backup", payload: asynqBackupPayload{}},
		{name: "bulk import", payload: asynqBulkImportPayload{}},
		{name: "legacy import", payload: asynqLegacyImportPayload{}},
		{name: "import row", payload: asynqImportRowPayload{}},
		{name: "import relationship", payload: asynqImportRelationshipPayload{}},
		{name: "media post process", payload: asynqMediaPostProcessPayload{}},
		{name: "media attachment", payload: asynqMediaAttachmentPayload{}},
		{name: "cache buster", payload: asynqCacheBusterPayload{}},
		{name: "announcement reaction", payload: asynqAnnouncementReactionPayload{}},
		{name: "removal", payload: asynqRemovalPayload{}},
		{name: "conversation", payload: asynqConversationPayload{}},
		{name: "push update", payload: asynqPushUpdatePayload{}},
		{name: "status account", payload: asynqStatusAccountPayload{}},
		{name: "authorize follow", payload: asynqAuthorizeFollowPayload{}},
		{name: "relationship", payload: asynqRelationshipPayload{}},
		{name: "mute", payload: asynqMutePayload{}},
		{name: "featured tag", payload: asynqFeaturedTagPayload{}},
		{name: "tag unmerge", payload: asynqTagUnmergePayload{}},
		{name: "unfollow follow", payload: asynqUnfollowFollowPayload{}},
		{name: "trigger webhook", payload: asynqTriggerWebhookPayload{}},
		{name: "webhook delivery", payload: asynqWebhookDeliveryPayload{}},
		{name: "web push notification", payload: asynqWebPushNotificationPayload{}},
		{name: "domain block", payload: asynqDomainBlockPayload{}},
		{name: "remote account refresh", payload: asynqRemoteAccountRefreshPayload{}},
		{name: "account refresh", payload: asynqAccountRefreshPayload{}},
		{name: "ActivityPub inbox processing", payload: activityPubInboxProcessingJob{}},
		{name: "ActivityPub delivery", payload: activityPubDeliveryRetryJob{}},
	}

	for _, test := range payloads {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := marshalAsynqTaskPayload(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Version int `json:"version"`
			}
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Version != asynqPayloadVersion43 {
				t.Fatalf("version = %d, want %d; payload=%s", envelope.Version, asynqPayloadVersion43, encoded)
			}
		})
	}
}

func TestAllAsynqTaskConstructorsUseVersionedMarshaller(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			hasTaskConstructor := false
			hasVersionedMarshaller := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch calledFunctionName(call.Fun) {
				case "asynq.NewTask", "asynq.NewTaskWithHeaders":
					hasTaskConstructor = true
				case "marshalAsynqTaskPayload":
					hasVersionedMarshaller = true
				}
				return true
			})
			if hasTaskConstructor && !hasVersionedMarshaller {
				t.Errorf("%s:%s constructs an Asynq task without marshalAsynqTaskPayload", path, function.Name.Name)
			}
		}
	}
}

func calledFunctionName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		qualifier, ok := expression.X.(*ast.Ident)
		if !ok {
			return expression.Sel.Name
		}
		return qualifier.Name + "." + expression.Sel.Name
	default:
		return ""
	}
}

func TestAsynqPayloadVersionMiddlewareAcceptsLegacyAndCurrent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "legacy absent version", payload: `{"account_id":1}`},
		{name: "legacy explicit zero", payload: `{"version":0,"account_id":1}`},
		{name: "current", payload: `{"version":1,"account_id":1}`},
		{name: "malformed JSON remains handler-owned", payload: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := asynqPayloadVersionMiddleware()(asynq.HandlerFunc(func(context.Context, *asynq.Task) error {
				called = true
				return nil
			}))
			if err := handler.ProcessTask(context.Background(), asynq.NewTask("test", []byte(test.payload))); err != nil {
				t.Fatalf("payload rejected: %v", err)
			}
			if !called {
				t.Fatal("payload did not reach the concrete handler")
			}
		})
	}
}

func TestEveryRegisteredAsynqHandlerRejectsUnknownFuturePayload(t *testing.T) {
	taskTypes := []string{
		asynqTaskRedownloadAvatar,
		asynqTaskRedownloadHeader,
		asynqTaskRedownloadMedia,
		asynqTaskRefollow,
		asynqTaskFetchReply,
		asynqTaskFetchReplies,
		asynqTaskFetchAllReplies,
		asynqTaskThreadResolve,
		asynqTaskMentionResolve,
		asynqTaskFeedInsert,
		asynqTaskLocalNotification,
		asynqTaskFilteredNotificationCleanup,
		asynqTaskUnfilterNotifications,
		asynqTaskGenerateAnnualReport,
		asynqTaskNotificationMail,
		asynqTaskConfirmationMail,
		asynqTaskMailerDelivery,
		asynqTaskDistributeAnnouncement,
		asynqTaskDistributeTermsOfService,
		asynqTaskBackup,
		asynqTaskBulkImport,
		asynqTaskLegacyImport,
		asynqTaskImportRow,
		asynqTaskImportRelationship,
		asynqTaskLinkCrawl,
		asynqTaskPostProcessMedia,
		asynqTaskRemoveFeaturedTag,
		asynqTaskTagUnmerge,
		asynqTaskUnfollowFollow,
		asynqTaskPublishScheduledStatus,
		asynqTaskPublishAnnouncement,
		asynqTaskUnpublishAnnouncement,
		asynqTaskRemoteAccountRefresh,
		asynqTaskAccountRefresh,
		asynqTaskAccountMerging,
		asynqTaskResolveAccount,
		asynqTaskPollExpiration,
		asynqTaskPollUpdate,
		asynqTaskAccountUpdate,
		asynqTaskRawDistribution,
		asynqTaskAccountRawDistribution,
		asynqTaskFeaturedCollectionSync,
		asynqTaskFeaturedTagsSync,
		asynqTaskMoveDistribution,
		asynqTaskPostUpgrade,
		asynqTaskFollowersSync,
		asynqTaskActivityPubProcessing,
		asynqTaskActivityPubDelivery,
		asynqTaskSelfDestructDelivery,
		asynqTaskActivityPubDistribution,
		asynqTaskStatusUpdateDistribution,
		asynqTaskCacheBuster,
		asynqTaskAnnouncementReaction,
		asynqTaskRemoval,
		asynqTaskPushConversation,
		asynqTaskPushUpdate,
		asynqTaskWebPushNotification,
		asynqTaskAuthorizeFollow,
		asynqTaskBootstrapTimeline,
		asynqTaskRegeneration,
		asynqTaskVerifyAccountLinks,
		asynqTaskTriggerWebhook,
		asynqTaskWebhookDelivery,
		asynqTaskDomainBlock,
		asynqTaskDomainClearMedia,
		asynqTaskAdminDomainPurge,
		asynqTaskAccountDeletion,
		asynqTaskAdminAccountDeletion,
		asynqTaskAdminSuspension,
		asynqTaskAdminUnsuspension,
		asynqTaskBlock,
		asynqTaskMute,
		asynqTaskMerge,
		asynqTaskUnmerge,
		asynqTaskDeleteMute,
		asynqTaskUnfavourite,
		asynqTaskAfterAccountDomainBlock,
		asynqTaskAfterUnallowDomain,
		asynqTaskFASPBackfill,
		asynqTaskFASPAccountSearch,
		asynqTaskFASPFollowRecommend,
		asynqTaskFASPAccountLifecycle,
		asynqTaskFASPContentLifecycle,
		asynqTaskFASPTrend,
	}

	mux := (&Server{}).newAsynqServeMux()
	seen := make(map[string]struct{}, len(taskTypes))
	for _, taskType := range taskTypes {
		if _, duplicate := seen[taskType]; duplicate {
			t.Fatalf("duplicate task type in test inventory: %s", taskType)
		}
		seen[taskType] = struct{}{}
		err := mux.ProcessTask(context.Background(), asynq.NewTask(taskType, []byte(`{"version":2}`)))
		if !errors.Is(err, asynq.SkipRetry) {
			t.Errorf("%s future payload error = %v, want SkipRetry", taskType, err)
		}
	}
}

func TestInvalidAsynqPayloadVersionIsPermanent(t *testing.T) {
	handler := asynqPayloadVersionMiddleware()(asynq.HandlerFunc(func(context.Context, *asynq.Task) error {
		t.Fatal("invalid version reached concrete handler")
		return nil
	}))
	for _, payload := range []string{`{"version":"1"}`, `{"version":null}`, `{"version":2}`} {
		if err := handler.ProcessTask(context.Background(), asynq.NewTask("test", []byte(payload))); !errors.Is(err, asynq.SkipRetry) {
			t.Errorf("payload %s error = %v, want SkipRetry", payload, err)
		}
	}
}

func TestRedisFallbackPayloadsRemainVersionless(t *testing.T) {
	payloads := []struct {
		name    string
		payload any
	}{
		{name: "ActivityPub inbox processing", payload: activityPubInboxProcessingJob{Body: json.RawMessage(`{}`)}},
		{name: "ActivityPub delivery", payload: activityPubDeliveryRetryJob{Body: json.RawMessage(`{}`)}},
		{name: "remote media retry", payload: remoteMediaRedownloadRetryJob{}},
		{name: "web push retry", payload: webPushDeliveryRetryJob{}},
		{name: "webhook retry", payload: webhookDeliveryRetryJob{}},
	}
	for _, test := range payloads {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatal(err)
			}
			if _, versioned := object["version"]; versioned {
				t.Fatalf("non-Asynq Redis payload unexpectedly changed: %s", encoded)
			}
		})
	}
}
