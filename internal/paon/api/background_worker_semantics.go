package api

type backgroundWorkerConcurrency string

const (
	backgroundWorkerSingleton     backgroundWorkerConcurrency = "singleton"
	backgroundWorkerRowClaimed    backgroundWorkerConcurrency = "row-claimed"
	backgroundWorkerDuplicateSafe backgroundWorkerConcurrency = "idempotent-duplicate-safe"
	backgroundWorkerQueueConsumer backgroundWorkerConcurrency = "queue-consumer"
)

type backgroundWorkerSemantics struct {
	Concurrency backgroundWorkerConcurrency
	Proof       string
}

// Keep this inventory in lockstep with StartBackgroundWorkers. It makes the
// multi-replica contract reviewable instead of relying on ticker placement.
var backgroundWorkerConcurrencyInventory = map[string]backgroundWorkerSemantics{
	"runPaonGoWorkerHeartbeat":            {Concurrency: backgroundWorkerDuplicateSafe, Proof: "per-process liveness key"},
	"syncMeiliIndexesBestEffort":          {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runStatsDInformantWorker":            {Concurrency: backgroundWorkerDuplicateSafe, Proof: "per-process metrics"},
	"runActivityPubDeliveryRetryWorker":   {Concurrency: backgroundWorkerQueueConsumer, Proof: "Redis visibility lease and owner-fenced ack"},
	"runActivityPubInboxProcessingWorker": {Concurrency: backgroundWorkerQueueConsumer, Proof: "legacy Redis queue migration into Asynq with owner-fenced ack"},
	"startAsynqWorker":                    {Concurrency: backgroundWorkerQueueConsumer, Proof: "Asynq reservation and retry"},
	"runWebhookDeliveryRetryWorker":       {Concurrency: backgroundWorkerQueueConsumer, Proof: "Redis visibility lease and owner-fenced ack"},
	"runWebPushDeliveryRetryWorker":       {Concurrency: backgroundWorkerQueueConsumer, Proof: "Redis visibility lease and owner-fenced ack"},
	"runScheduledStatusPublishWorker":     {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker and deterministic task ID"},
	"runStatusesCleanupWorker":            {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runPollExpirationWorker":             {Concurrency: backgroundWorkerRowClaimed, Proof: "deterministic final-check task and notification row uniqueness"},
	"runMuteExpirationWorker":             {Concurrency: backgroundWorkerRowClaimed, Proof: "conditional delete"},
	"runAccountDeletionWorker":            {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker and unique task"},
	"runBackupVacuumWorker":               {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runOAuthVacuumWorker":                {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runImportVacuumWorker":               {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runFeedVacuumWorker":                 {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runPreviewCardVacuumWorker":          {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runMediaVacuumWorker":                {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runMediaPostProcessWorker":           {Concurrency: backgroundWorkerRowClaimed, Proof: "conditional processing-state update"},
	"runRemoteMediaRedownloadWorker":      {Concurrency: backgroundWorkerQueueConsumer, Proof: "Redis visibility lease and owner-fenced ack"},
	"runStatusVacuumWorker":               {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runFollowRecommendationsWorker":      {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runIPCleanupWorker":                  {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runUserCleanupWorker":                {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runAutoCloseRegistrationsWorker":     {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker and PostgreSQL advisory transition lock"},
	"runPgHeroSpaceStatsWorker":           {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runAdminMetricsPrewarmWorker":        {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runInstanceRefreshWorker":            {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runIndexingWorker":                   {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runSoftwareUpdateCheckWorker":        {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runTrendsRefreshWorker":              {Concurrency: backgroundWorkerSingleton, Proof: "separate refresh and review cadence markers"},
	"runFeaturedTagRefreshWorker":         {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
	"runProfileVerificationWorker":        {Concurrency: backgroundWorkerSingleton, Proof: "scheduler cadence marker"},
}
