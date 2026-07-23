package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/redis/go-redis/v9"
)

const (
	asynqHistoryDays  = 90
	asynqTaskPageSize = 50
	asynqTaskMaxPage  = 200
)

var (
	errUnknownAsynqQueue          = errors.New("unknown asynq queue")
	errUnsupportedAsynqRetryState = errors.New("unsupported asynq retry state")
	errAsynqTaskStateChanged      = errors.New("asynq task state changed")
)

// asynqInspectorClient is deliberately limited to the operations used by the
// dashboard. Keeping the Inspector behind the Server makes the operational
// views and actions testable without a package-global factory or a second Redis
// client.
type asynqInspectorClient interface {
	Close() error
	Queues() ([]string, error)
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
	History(queue string, days int) ([]*asynq.DailyStats, error)
	Servers() ([]*asynq.ServerInfo, error)
	ListPendingTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListActiveTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListRetryTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListScheduledTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListArchivedTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	RunAllRetryTasks(queue string) (int, error)
	RunAllArchivedTasks(queue string) (int, error)
}

type asynqTaskRetryer interface {
	RetryTask(ctx context.Context, queue string, taskID string, expectedState string) error
	Close() error
}

type asynqTaskTransitionRunner func(ctx context.Context, keys []string, args ...interface{}) (int64, error)

type redisAsynqTaskRetryer struct {
	client redis.UniversalClient
	run    asynqTaskTransitionRunner
}

const retryAsynqTaskFromStateScript = `
local state = redis.call("HGET", KEYS[1], "state")
if not state then
  return -1
end
if state ~= ARGV[2] then
  return -2
end
if redis.call("ZREM", KEYS[2], ARGV[1]) == 0 then
  return -3
end
redis.call("LPUSH", KEYS[3], ARGV[1])
redis.call("HSET", KEYS[1], "state", "pending")
return 1
`

const asynqTaskRetryTimeout = 5 * time.Second

var retryAsynqTaskFromStateRedisScript = redis.NewScript(retryAsynqTaskFromStateScript)

type asynqDashboardSummary struct {
	ProcessedTotal int `json:"processed_total"`
	FailedTotal    int `json:"failed_total"`
	Active         int `json:"active"`
	Pending        int `json:"pending"`
	Retry          int `json:"retry"`
	Scheduled      int `json:"scheduled"`
	Archived       int `json:"archived"`
}

type asynqDashboardIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Label    string `json:"label"`
	Detail   string `json:"detail"`
	Queue    string `json:"queue,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
}

type asynqQueueView struct {
	Name              string                `json:"name"`
	DisplayName       string                `json:"display_name"`
	Size              int                   `json:"size"`
	Pending           int                   `json:"pending"`
	Active            int                   `json:"active"`
	Retry             int                   `json:"retry"`
	Scheduled         int                   `json:"scheduled"`
	Archived          int                   `json:"archived"`
	Aggregating       int                   `json:"aggregating"`
	Completed         int                   `json:"completed"`
	ProcessedTotal    int                   `json:"processed_total"`
	FailedTotal       int                   `json:"failed_total"`
	Latency           string                `json:"latency"`
	LatencySeconds    float64               `json:"latency_seconds"`
	Memory            string                `json:"memory"`
	MemoryBytes       int64                 `json:"memory_bytes"`
	Paused            bool                  `json:"paused"`
	ActiveConsumers   int                   `json:"active_consumers"`
	ConsumerCapacity  int                   `json:"consumer_capacity"`
	ConsumerActive    int                   `json:"consumer_active"`
	ConsumerAvailable bool                  `json:"consumer_available"`
	Status            string                `json:"status"`
	StatusLabel       string                `json:"status_label"`
	Issues            []asynqDashboardIssue `json:"issues"`
}

type asynqWorkerView struct {
	TaskID    string `json:"task_id"`
	TaskType  string `json:"task_type"`
	Queue     string `json:"queue"`
	StartedAt string `json:"started_at"`
	Elapsed   string `json:"elapsed"`
	Deadline  string `json:"deadline,omitempty"`
	Orphaned  bool   `json:"orphaned"`
}

type asynqServerView struct {
	ID          string            `json:"id"`
	Host        string            `json:"host"`
	PID         int               `json:"pid"`
	Status      string            `json:"status"`
	Concurrency int               `json:"concurrency"`
	Active      int               `json:"active"`
	Utilization float64           `json:"utilization"`
	StartedAt   string            `json:"started_at"`
	Queues      []string          `json:"queues"`
	Workers     []asynqWorkerView `json:"workers"`
}

type asynqHistoryView struct {
	Date      string `json:"date"`
	Processed int    `json:"processed"`
	Failed    int    `json:"failed"`
	Succeeded int    `json:"succeeded"`
}

type asynqDashboardSnapshot struct {
	Timestamp string                `json:"timestamp"`
	Available bool                  `json:"available"`
	Summary   asynqDashboardSummary `json:"summary"`
	Queues    []asynqQueueView      `json:"queues"`
	Servers   []asynqServerView     `json:"servers"`
	Issues    []asynqDashboardIssue `json:"issues"`
	History   []asynqHistoryView    `json:"history"`
	Error     string                `json:"error,omitempty"`
}

type asynqDashboardData struct {
	Snapshot     asynqDashboardSnapshot
	QueueInfos   map[string]*asynq.QueueInfo
	ActiveTasks  map[string][]*asynq.TaskInfo
	WorkerByTask map[string]*asynq.WorkerInfo
	ServerByTask map[string]*asynq.ServerInfo
}

type asynqTaskView struct {
	ID            string
	Queue         string
	Type          string
	Retried       int
	MaxRetry      int
	LastError     string
	LastFailedAt  time.Time
	NextProcessAt time.Time
	Deadline      time.Time
	StartedAt     time.Time
	Elapsed       time.Duration
	IsOrphaned    bool
	PayloadBytes  int
	RawPayload    string
	WorkerHost    string
	WorkerPID     int
	Sequence      int
}

type asynqTaskPage struct {
	State       string
	Queue       string
	Page        int
	PageSize    int
	Total       int
	Tasks       []asynqTaskView
	HasPrevious bool
	HasNext     bool
	Truncated   bool
	ResultLimit int
}

func asynqTaskKey(queue string, taskID string) string {
	return queue + "\x00" + taskID
}

func asynqLogicalQueueName(namespace string, queue string) string {
	prefix := strings.TrimSuffix(strings.TrimSpace(namespace), ":")
	if prefix != "" {
		prefix += ":"
		if strings.HasPrefix(queue, prefix) {
			return strings.TrimPrefix(queue, prefix)
		}
	}
	return queue
}

func asynqConfiguredQueueNames(s *Server) []string {
	if s == nil {
		return nil
	}
	weights := paonGoAsynqQueueWeightsForConfig(s.cfg)
	queues := make([]string, 0, len(weights))
	for queue := range weights {
		queues = append(queues, queue)
	}
	sort.Strings(queues)
	return queues
}

func asynqQueueOwnedByServer(s *Server, queue string) bool {
	if s == nil || strings.TrimSpace(queue) == "" {
		return false
	}
	for _, configured := range asynqConfiguredQueueNames(s) {
		if queue == configured {
			return true
		}
	}
	namespacePrefix := strings.TrimSuffix(strings.TrimSpace(s.cfg.RedisNamespace), ":")
	if namespacePrefix == "" {
		return false
	}
	return strings.HasPrefix(queue, namespacePrefix+":")
}

func asynqQueueRedisKeyPrefix(queue string) string {
	return "asynq:{" + queue + "}:"
}

func newRedisAsynqTaskRetryer(cfg config.Config) (*redisAsynqTaskRetryer, error) {
	opt, ok := asynqRedisOpt(cfg).(asynq.RedisClientOpt)
	if !ok {
		return nil, errors.New("asynq task retry requires a direct Redis client")
	}
	client := redis.NewClient(&redis.Options{
		Network:      opt.Network,
		Addr:         opt.Addr,
		Username:     opt.Username,
		Password:     opt.Password,
		DB:           opt.DB,
		DialTimeout:  opt.DialTimeout,
		ReadTimeout:  opt.ReadTimeout,
		WriteTimeout: opt.WriteTimeout,
		PoolSize:     opt.PoolSize,
		TLSConfig:    opt.TLSConfig,
		// A timed-out mutation may already have reached Redis. Retrying at the
		// transport layer could execute a fast-failing task twice.
		MaxRetries: -1,
	})
	return &redisAsynqTaskRetryer{client: client}, nil
}

func (r *redisAsynqTaskRetryer) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}

func (r redisAsynqTaskRetryer) RetryTask(ctx context.Context, queue string, taskID string, expectedState string) error {
	ctx, cancel := context.WithTimeout(ctx, asynqTaskRetryTimeout)
	defer cancel()
	keys := []string{
		asynqQueueRedisKeyPrefix(queue) + "t:" + taskID,
		asynqQueueRedisKeyPrefix(queue) + expectedState,
		asynqQueueRedisKeyPrefix(queue) + "pending",
	}
	run := r.run
	if run == nil {
		if r.client == nil {
			return errors.New("asynq task retry Redis client is unavailable")
		}
		run = func(ctx context.Context, keys []string, args ...interface{}) (int64, error) {
			return retryAsynqTaskFromStateRedisScript.Run(ctx, r.client, keys, args...).Int64()
		}
	}
	code, err := run(ctx, keys, taskID, expectedState)
	if err != nil {
		return err
	}
	switch code {
	case 1:
		return nil
	case -1:
		return asynq.ErrTaskNotFound
	case -2, -3:
		return errAsynqTaskStateChanged
	default:
		return fmt.Errorf("unexpected Asynq retry response %d", code)
	}
}

func asynqDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	if value < time.Second {
		return "<1s"
	}
	return value.Round(time.Second).String()
}

func asynqUnavailableSnapshot(locale string, err error) asynqDashboardSnapshot {
	detail := adminT(locale, "admin.devops.unavailable_detail", "Asynq data could not be loaded. The displayed counts are unavailable.")
	_ = err // Keep Redis addresses and other connection details out of the browser.
	return asynqDashboardSnapshot{
		Timestamp: "",
		Available: false,
		Queues:    []asynqQueueView{},
		Servers:   []asynqServerView{},
		Issues:    []asynqDashboardIssue{},
		History:   []asynqHistoryView{},
		Error:     detail,
	}
}

func asynqIssue(locale string, severity string, code string, queue string, taskID string, detail string) asynqDashboardIssue {
	labels := map[string][2]string{
		"paused":            {"admin.devops.issue_paused", "Queue paused"},
		"no_consumer":       {"admin.devops.issue_no_consumer", "No active consumer"},
		"saturated":         {"admin.devops.issue_saturated", "Worker capacity saturated"},
		"orphaned":          {"admin.devops.issue_orphaned", "Orphaned active task"},
		"deadline_exceeded": {"admin.devops.issue_deadline_exceeded", "Task deadline exceeded"},
		"retry":             {"admin.devops.issue_retry", "Tasks awaiting retry"},
		"archived":          {"admin.devops.issue_archived", "Archived tasks need attention"},
	}
	label := code
	if translation, ok := labels[code]; ok {
		label = adminT(locale, translation[0], translation[1])
	}
	return asynqDashboardIssue{Severity: severity, Code: code, Label: label, Detail: detail, Queue: queue, TaskID: taskID}
}

func asynqIssueSeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}

func asynqSortIssues(issues []asynqDashboardIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		left, right := issues[i], issues[j]
		if asynqIssueSeverityRank(left.Severity) != asynqIssueSeverityRank(right.Severity) {
			return asynqIssueSeverityRank(left.Severity) > asynqIssueSeverityRank(right.Severity)
		}
		if left.Queue != right.Queue {
			return left.Queue < right.Queue
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.TaskID < right.TaskID
	})
}

func (s *Server) collectAsynqDashboard(locale string) (*asynqDashboardData, error) {
	if s == nil || s.asynqInspector == nil {
		return nil, errors.New("asynq inspector is unavailable")
	}
	configured := make(map[string]struct{})
	for _, queue := range asynqConfiguredQueueNames(s) {
		configured[queue] = struct{}{}
	}

	discoveredQueues, err := s.asynqInspector.Queues()
	if err != nil {
		return nil, fmt.Errorf("list asynq queues: %w", err)
	}
	discovered := make(map[string]struct{}, len(discoveredQueues))
	namespacePrefix := strings.TrimSuffix(strings.TrimSpace(s.cfg.RedisNamespace), ":")
	if namespacePrefix != "" {
		namespacePrefix += ":"
	}
	for _, queue := range discoveredQueues {
		if namespacePrefix != "" {
			if !strings.HasPrefix(queue, namespacePrefix) {
				continue
			}
		} else if _, owned := configured[queue]; !owned {
			// Without an application namespace, queue names are the only safe
			// ownership boundary. Do not expose another Asynq application's data.
			continue
		}
		discovered[queue] = struct{}{}
		configured[queue] = struct{}{}
	}
	physicalQueues := make([]string, 0, len(configured))
	for queue := range configured {
		physicalQueues = append(physicalQueues, queue)
	}
	sort.Strings(physicalQueues)

	data := &asynqDashboardData{
		Snapshot: asynqDashboardSnapshot{
			Available: true,
			Queues:    make([]asynqQueueView, 0, len(physicalQueues)),
			Servers:   []asynqServerView{},
			Issues:    []asynqDashboardIssue{},
			History:   []asynqHistoryView{},
		},
		QueueInfos:   make(map[string]*asynq.QueueInfo, len(physicalQueues)),
		ActiveTasks:  make(map[string][]*asynq.TaskInfo, len(physicalQueues)),
		WorkerByTask: make(map[string]*asynq.WorkerInfo),
		ServerByTask: make(map[string]*asynq.ServerInfo),
	}

	for _, queue := range physicalQueues {
		info := &asynq.QueueInfo{Queue: queue}
		if _, ok := discovered[queue]; ok {
			info, err = s.asynqInspector.GetQueueInfo(queue)
			if errors.Is(err, asynq.ErrQueueNotFound) {
				// Discovery and stats are separate reads; retry once if the queue moved
				// during the snapshot rather than reporting a healthy zero.
				info, err = s.asynqInspector.GetQueueInfo(queue)
			}
			if err != nil {
				return nil, fmt.Errorf("read asynq queue info: %w", err)
			}
		}
		data.QueueInfos[queue] = info
		data.Snapshot.Summary.ProcessedTotal += info.ProcessedTotal
		data.Snapshot.Summary.FailedTotal += info.FailedTotal
		data.Snapshot.Summary.Active += info.Active
		data.Snapshot.Summary.Pending += info.Pending
		data.Snapshot.Summary.Retry += info.Retry
		data.Snapshot.Summary.Scheduled += info.Scheduled
		data.Snapshot.Summary.Archived += info.Archived
	}

	servers, err := s.asynqInspector.Servers()
	if err != nil {
		return nil, fmt.Errorf("read asynq servers: %w", err)
	}
	nonNilServers := servers[:0]
	for _, server := range servers {
		if server != nil {
			nonNilServers = append(nonNilServers, server)
		}
	}
	servers = nonNilServers
	sort.SliceStable(servers, func(i, j int) bool {
		if servers[i].Host != servers[j].Host {
			return servers[i].Host < servers[j].Host
		}
		if servers[i].PID != servers[j].PID {
			return servers[i].PID < servers[j].PID
		}
		return servers[i].ID < servers[j].ID
	})

	relevantServers := make([]*asynq.ServerInfo, 0, len(servers))
	for _, server := range servers {
		queueNames := make([]string, 0, len(server.Queues))
		for queue := range server.Queues {
			if _, ok := configured[queue]; ok {
				queueNames = append(queueNames, asynqLogicalQueueName(s.cfg.RedisNamespace, queue))
			}
		}
		if len(queueNames) == 0 {
			continue
		}
		relevantServers = append(relevantServers, server)
		sort.Strings(queueNames)

		workers := make([]asynqWorkerView, 0, len(server.ActiveWorkers))
		for _, worker := range server.ActiveWorkers {
			if worker == nil {
				continue
			}
			if _, ok := configured[worker.Queue]; !ok {
				continue
			}
			key := asynqTaskKey(worker.Queue, worker.TaskID)
			data.WorkerByTask[key] = worker
			data.ServerByTask[key] = server
			deadline := ""
			if !worker.Deadline.IsZero() {
				deadline = worker.Deadline.UTC().Format(time.RFC3339)
			}
			startedAt := ""
			if !worker.Started.IsZero() {
				startedAt = worker.Started.UTC().Format(time.RFC3339)
			}
			workers = append(workers, asynqWorkerView{
				TaskID:    worker.TaskID,
				TaskType:  worker.TaskType,
				Queue:     asynqLogicalQueueName(s.cfg.RedisNamespace, worker.Queue),
				StartedAt: startedAt,
				Deadline:  deadline,
			})
		}
		sort.SliceStable(workers, func(i, j int) bool {
			if workers[i].StartedAt != workers[j].StartedAt {
				return workers[i].StartedAt < workers[j].StartedAt
			}
			return workers[i].Queue+workers[i].TaskID < workers[j].Queue+workers[j].TaskID
		})
		utilization := 0.0
		if server.Concurrency > 0 {
			utilization = math.Min(100, float64(len(server.ActiveWorkers))*100/float64(server.Concurrency))
		}
		serverStartedAt := ""
		if !server.Started.IsZero() {
			serverStartedAt = server.Started.UTC().Format(time.RFC3339)
		}
		data.Snapshot.Servers = append(data.Snapshot.Servers, asynqServerView{
			ID:          server.ID,
			Host:        server.Host,
			PID:         server.PID,
			Status:      server.Status,
			Concurrency: server.Concurrency,
			Active:      len(server.ActiveWorkers),
			Utilization: math.Round(utilization*10) / 10,
			StartedAt:   serverStartedAt,
			Queues:      queueNames,
			Workers:     workers,
		})
	}

	for _, queue := range physicalQueues {
		info := data.QueueInfos[queue]
		if info.Active == 0 {
			data.ActiveTasks[queue] = []*asynq.TaskInfo{}
			continue
		}
		tasks, listErr := s.asynqInspector.ListActiveTasks(queue, asynq.Page(1), asynq.PageSize(info.Active))
		if listErr != nil {
			return nil, fmt.Errorf("read active asynq tasks: %w", listErr)
		}
		data.ActiveTasks[queue] = tasks
	}

	now := time.Now().UTC()
	for i := range data.Snapshot.Servers {
		for j := range data.Snapshot.Servers[i].Workers {
			workerView := &data.Snapshot.Servers[i].Workers[j]
			worker := data.WorkerByTask[asynqTaskKey(asynqQueueName(s.cfg, workerView.Queue), workerView.TaskID)]
			if worker != nil && !worker.Started.IsZero() {
				workerView.Elapsed = asynqDuration(now.Sub(worker.Started))
			}
		}
	}

	historyByDate := make(map[string]*asynqHistoryView, asynqHistoryDays)
	for day := asynqHistoryDays - 1; day >= 0; day-- {
		date := now.AddDate(0, 0, -day).Format("2006-01-02")
		historyByDate[date] = &asynqHistoryView{Date: date}
	}
	for _, queue := range physicalQueues {
		if _, ok := discovered[queue]; !ok {
			continue
		}
		history, historyErr := s.asynqInspector.History(queue, asynqHistoryDays)
		if historyErr != nil {
			return nil, fmt.Errorf("read asynq history: %w", historyErr)
		}
		for _, daily := range history {
			if daily == nil {
				continue
			}
			date := daily.Date.UTC().Format("2006-01-02")
			row, ok := historyByDate[date]
			if !ok {
				continue
			}
			row.Processed += daily.Processed
			row.Failed += daily.Failed
		}
	}
	for _, row := range historyByDate {
		row.Succeeded = row.Processed - row.Failed
		if row.Succeeded < 0 {
			row.Succeeded = 0
		}
		data.Snapshot.History = append(data.Snapshot.History, *row)
	}
	sort.Slice(data.Snapshot.History, func(i, j int) bool { return data.Snapshot.History[i].Date < data.Snapshot.History[j].Date })

	for _, queue := range physicalQueues {
		info := data.QueueInfos[queue]
		displayName := asynqLogicalQueueName(s.cfg.RedisNamespace, queue)
		queueIssues := make([]asynqDashboardIssue, 0)
		activeConsumers := make([]*asynq.ServerInfo, 0)
		for _, server := range relevantServers {
			if server.Status != "active" {
				continue
			}
			if _, ok := server.Queues[queue]; ok {
				activeConsumers = append(activeConsumers, server)
			}
		}
		if info.Paused {
			queueIssues = append(queueIssues, asynqIssue(locale, "critical", "paused", displayName, "", adminT(locale, "admin.devops.detail_paused", "This queue is paused and will not process tasks.")))
		}
		if info.Pending > 0 && len(activeConsumers) == 0 {
			queueIssues = append(queueIssues, asynqIssue(locale, "critical", "no_consumer", displayName, "", fmt.Sprintf(adminT(locale, "admin.devops.detail_no_consumer", "%d pending tasks have no active consumer."), info.Pending)))
		}
		if info.Pending > 0 && info.Active > 0 && len(activeConsumers) > 0 {
			allSaturated := true
			for _, server := range activeConsumers {
				if server.Concurrency <= 0 || len(server.ActiveWorkers) < server.Concurrency {
					allSaturated = false
					break
				}
			}
			if allSaturated {
				queueIssues = append(queueIssues, asynqIssue(locale, "warning", "saturated", displayName, "", fmt.Sprintf(adminT(locale, "admin.devops.detail_saturated", "%d pending tasks are waiting while all consumers are at capacity."), info.Pending)))
			}
		}
		consumerCapacity := 0
		consumerActive := 0
		for _, server := range activeConsumers {
			consumerCapacity += max(server.Concurrency, 0)
			consumerActive += len(server.ActiveWorkers)
		}
		if info.Retry > 0 {
			queueIssues = append(queueIssues, asynqIssue(locale, "warning", "retry", displayName, "", fmt.Sprintf(adminT(locale, "admin.devops.detail_retry", "%d tasks are awaiting retry."), info.Retry)))
		}
		if info.Archived > 0 {
			queueIssues = append(queueIssues, asynqIssue(locale, "critical", "archived", displayName, "", fmt.Sprintf(adminT(locale, "admin.devops.detail_archived", "%d tasks are archived."), info.Archived)))
		}
		for _, task := range data.ActiveTasks[queue] {
			if task != nil && task.IsOrphaned {
				queueIssues = append(queueIssues, asynqIssue(locale, "critical", "orphaned", displayName, task.ID, adminT(locale, "admin.devops.detail_orphaned", "An active task has no worker lease.")))
			}
		}
		for key, worker := range data.WorkerByTask {
			if worker == nil || worker.Queue != queue || worker.Deadline.IsZero() || !worker.Deadline.Before(now) {
				continue
			}
			_ = key
			queueIssues = append(queueIssues, asynqIssue(locale, "critical", "deadline_exceeded", displayName, worker.TaskID, adminT(locale, "admin.devops.detail_deadline_exceeded", "A running task is past its effective deadline.")))
		}
		asynqSortIssues(queueIssues)
		status := "healthy"
		statusLabel := adminT(locale, "admin.devops.healthy", "Healthy")
		if len(queueIssues) > 0 {
			status = queueIssues[0].Severity
			statusLabel = adminT(locale, "admin.devops.attention", "Attention")
		} else if info.Pending+info.Active+info.Retry+info.Scheduled+info.Archived+info.Aggregating == 0 {
			status = "idle"
			statusLabel = adminT(locale, "admin.devops.idle", "Idle")
		}
		view := asynqQueueView{
			Name:              queue,
			DisplayName:       displayName,
			Size:              info.Pending + info.Active + info.Retry + info.Scheduled + info.Archived + info.Aggregating,
			Pending:           info.Pending,
			Active:            info.Active,
			Retry:             info.Retry,
			Scheduled:         info.Scheduled,
			Archived:          info.Archived,
			Aggregating:       info.Aggregating,
			Completed:         info.Completed,
			ProcessedTotal:    info.ProcessedTotal,
			FailedTotal:       info.FailedTotal,
			Latency:           asynqDuration(info.Latency),
			LatencySeconds:    info.Latency.Seconds(),
			Memory:            formatBytes(info.MemoryUsage),
			MemoryBytes:       info.MemoryUsage,
			Paused:            info.Paused,
			ActiveConsumers:   len(activeConsumers),
			ConsumerCapacity:  consumerCapacity,
			ConsumerActive:    consumerActive,
			ConsumerAvailable: len(activeConsumers) > 0,
			Status:            status,
			StatusLabel:       statusLabel,
			Issues:            queueIssues,
		}
		data.Snapshot.Queues = append(data.Snapshot.Queues, view)
		data.Snapshot.Issues = append(data.Snapshot.Issues, queueIssues...)
	}
	asynqSortIssues(data.Snapshot.Issues)
	data.Snapshot.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return data, nil
}

func asynqTaskStateCount(info *asynq.QueueInfo, state string) int {
	if info == nil {
		return 0
	}
	switch state {
	case "active":
		return info.Active
	case "pending":
		return info.Pending
	case "retry":
		return info.Retry
	case "scheduled":
		return info.Scheduled
	case "archived":
		return info.Archived
	default:
		return 0
	}
}

func asynqTaskListForState(inspector asynqInspectorClient, state string, queue string, limit int) ([]*asynq.TaskInfo, error) {
	return asynqTaskListPageForState(inspector, state, queue, 1, limit)
}

func asynqTaskListPageForState(inspector asynqInspectorClient, state string, queue string, page int, pageSize int) ([]*asynq.TaskInfo, error) {
	opts := []asynq.ListOption{asynq.Page(page), asynq.PageSize(pageSize)}
	switch state {
	case "pending":
		return inspector.ListPendingTasks(queue, opts...)
	case "active":
		return inspector.ListActiveTasks(queue, opts...)
	case "retry":
		return inspector.ListRetryTasks(queue, opts...)
	case "scheduled":
		return inspector.ListScheduledTasks(queue, opts...)
	case "archived":
		return inspector.ListArchivedTasks(queue, opts...)
	default:
		return nil, fmt.Errorf("unsupported asynq task state %q", state)
	}
}

func asynqArchivedTaskWindow(inspector asynqInspectorClient, queue string, count int, needed int) ([]*asynq.TaskInfo, error) {
	if count <= 0 || needed <= 0 {
		return []*asynq.TaskInfo{}, nil
	}
	needed = min(needed, count)
	start := count - needed
	pageSize := needed
	firstPage := start/pageSize + 1
	tasks, err := asynqTaskListPageForState(inspector, "archived", queue, firstPage, pageSize)
	if err != nil {
		return nil, err
	}
	firstPageEnd := firstPage * pageSize
	if firstPageEnd < count {
		next, nextErr := asynqTaskListPageForState(inspector, "archived", queue, firstPage+1, pageSize)
		if nextErr != nil {
			return nil, nextErr
		}
		tasks = append(tasks, next...)
	}
	if len(tasks) > needed {
		tasks = tasks[len(tasks)-needed:]
	}
	return tasks, nil
}

func asynqResolveQueueFilter(s *Server, data *asynqDashboardData, filter string) string {
	filter = strings.TrimSpace(filter)
	if filter == "" || s == nil || data == nil {
		return ""
	}
	queues := make([]string, 0, len(data.QueueInfos))
	for queue := range data.QueueInfos {
		queues = append(queues, queue)
	}
	sort.Strings(queues)
	for _, queue := range queues {
		if filter == queue {
			return queue
		}
	}
	match := ""
	for _, queue := range queues {
		if filter != asynqLogicalQueueName(s.cfg.RedisNamespace, queue) {
			continue
		}
		if match != "" {
			return ""
		}
		match = queue
	}
	return match
}

func (s *Server) asynqTaskPage(data *asynqDashboardData, state string, queueFilter string, page int) (asynqTaskPage, error) {
	if page < 1 {
		page = 1
	}
	if page > asynqTaskMaxPage {
		page = asynqTaskMaxPage
	}
	resolvedQueue := asynqResolveQueueFilter(s, data, queueFilter)
	if strings.TrimSpace(queueFilter) != "" && resolvedQueue == "" {
		return asynqTaskPage{}, errUnknownAsynqQueue
	}
	result := asynqTaskPage{State: state, Queue: resolvedQueue, Page: page, PageSize: asynqTaskPageSize}
	queues := make([]string, 0, len(data.QueueInfos))
	for queue := range data.QueueInfos {
		queues = append(queues, queue)
	}
	sort.Strings(queues)
	if result.Queue != "" {
		queues = []string{result.Queue}
	}
	for _, queue := range queues {
		result.Total += asynqTaskStateCount(data.QueueInfos[queue], state)
	}
	needed := page * asynqTaskPageSize
	allTasks := make([]asynqTaskView, 0, min(result.Total, needed*len(queues)))
	for _, queue := range queues {
		count := asynqTaskStateCount(data.QueueInfos[queue], state)
		if count == 0 {
			continue
		}
		limit := min(count, needed)
		var tasks []*asynq.TaskInfo
		if state == "active" {
			var ok bool
			tasks, ok = data.ActiveTasks[queue]
			if !ok {
				var err error
				tasks, err = asynqTaskListForState(s.asynqInspector, state, queue, count)
				if err != nil {
					return result, fmt.Errorf("read asynq %s tasks: %w", state, err)
				}
			}
		} else if state == "archived" {
			var err error
			tasks, err = asynqArchivedTaskWindow(s.asynqInspector, queue, count, needed)
			if err != nil {
				return result, fmt.Errorf("read asynq %s tasks: %w", state, err)
			}
		} else {
			var err error
			tasks, err = asynqTaskListForState(s.asynqInspector, state, queue, limit)
			if err != nil {
				return result, fmt.Errorf("read asynq %s tasks: %w", state, err)
			}
		}
		for sequence, task := range tasks {
			if task == nil {
				continue
			}
			worker := data.WorkerByTask[asynqTaskKey(queue, task.ID)]
			server := data.ServerByTask[asynqTaskKey(queue, task.ID)]
			deadline := task.Deadline
			startedAt := time.Time{}
			elapsed := time.Duration(0)
			if worker != nil && !worker.Started.IsZero() {
				startedAt = worker.Started
				elapsed = time.Since(worker.Started)
			}
			if worker != nil && !worker.Deadline.IsZero() {
				deadline = worker.Deadline
			}
			nextProcessAt := task.NextProcessAt
			if state == "pending" || state == "active" || state == "archived" {
				nextProcessAt = time.Time{}
			}
			view := asynqTaskView{
				ID:            task.ID,
				Queue:         queue,
				Type:          task.Type,
				Retried:       task.Retried,
				MaxRetry:      task.MaxRetry,
				LastError:     task.LastErr,
				LastFailedAt:  task.LastFailedAt,
				NextProcessAt: nextProcessAt,
				Deadline:      deadline,
				StartedAt:     startedAt,
				Elapsed:       elapsed,
				IsOrphaned:    task.IsOrphaned,
				PayloadBytes:  len(task.Payload),
				RawPayload:    string(task.Payload),
				Sequence:      sequence,
			}
			if server != nil {
				view.WorkerHost = server.Host
				view.WorkerPID = server.PID
			}
			allTasks = append(allTasks, view)
		}
	}

	sort.SliceStable(allTasks, func(i, j int) bool {
		left, right := allTasks[i], allTasks[j]
		switch state {
		case "active":
			if left.IsOrphaned != right.IsOrphaned {
				return left.IsOrphaned
			}
			if !left.StartedAt.Equal(right.StartedAt) {
				if left.StartedAt.IsZero() {
					return false
				}
				if right.StartedAt.IsZero() {
					return true
				}
				return left.StartedAt.Before(right.StartedAt)
			}
		case "retry", "scheduled":
			if !left.NextProcessAt.Equal(right.NextProcessAt) {
				return left.NextProcessAt.Before(right.NextProcessAt)
			}
		case "archived":
			if !left.LastFailedAt.Equal(right.LastFailedAt) {
				return left.LastFailedAt.After(right.LastFailedAt)
			}
		case "pending":
			if left.Queue == right.Queue && left.Sequence != right.Sequence {
				return left.Sequence < right.Sequence
			}
		}
		if left.Queue != right.Queue {
			return left.Queue < right.Queue
		}
		return left.ID < right.ID
	})
	start := (page - 1) * asynqTaskPageSize
	if start < len(allTasks) {
		end := min(start+asynqTaskPageSize, len(allTasks))
		result.Tasks = allTasks[start:end]
	} else {
		result.Tasks = []asynqTaskView{}
	}
	result.HasPrevious = page > 1
	result.HasNext = start+len(result.Tasks) < result.Total && page < asynqTaskMaxPage
	result.ResultLimit = asynqTaskMaxPage * asynqTaskPageSize
	result.Truncated = result.Total > result.ResultLimit
	return result, nil
}

func asynqViewForPath(path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	switch path {
	case "/asynq", "/sidekiq":
		return "overview", true
	case "/asynq/queues", "/sidekiq/queues":
		return "queues", true
	case "/asynq/active", "/sidekiq/busy", "/sidekiq/active":
		return "active", true
	case "/asynq/pending", "/sidekiq/pending":
		return "pending", true
	case "/asynq/retry", "/asynq/retries", "/sidekiq/retry", "/sidekiq/retries":
		return "retry", true
	case "/asynq/scheduled", "/sidekiq/scheduled":
		return "scheduled", true
	case "/asynq/archived", "/sidekiq/archived", "/sidekiq/morgue", "/sidekiq/dead":
		return "archived", true
	default:
		return "", false
	}
}

func asynqPageNumber(c *echo.Context) int {
	page, err := strconv.Atoi(c.QueryParam("page"))
	if err != nil || page < 1 {
		return 1
	}
	return min(page, asynqTaskMaxPage)
}

func asynqRetryTaskState(state string) (asynq.TaskState, bool) {
	switch state {
	case "retry":
		return asynq.TaskStateRetry, true
	case "archived":
		return asynq.TaskStateArchived, true
	default:
		return 0, false
	}
}

func (s *Server) runAsynqTaskNow(ctx context.Context, sourceState string, queue string, taskID string) error {
	_, ok := asynqRetryTaskState(sourceState)
	if !ok {
		return errUnsupportedAsynqRetryState
	}
	if !asynqQueueOwnedByServer(s, queue) {
		return errUnknownAsynqQueue
	}
	if strings.TrimSpace(taskID) == "" {
		return asynq.ErrTaskNotFound
	}
	if s.asynqTaskRetryer == nil {
		return errors.New("asynq task retryer is unavailable")
	}
	// The retryer compares and moves the task in one Redis operation. This keeps
	// a second click from rerunning a task that completed after the page loaded.
	return s.asynqTaskRetryer.RetryTask(ctx, queue, taskID, sourceState)
}

func (s *Server) runAllAsynqTasksNow(sourceState string, queueFilter string) (int, error) {
	if _, ok := asynqRetryTaskState(sourceState); !ok {
		return 0, errUnsupportedAsynqRetryState
	}
	if s == nil || s.asynqInspector == nil {
		return 0, errors.New("asynq inspector is unavailable")
	}

	queues := []string{}
	if queueFilter != "" {
		if !asynqQueueOwnedByServer(s, queueFilter) {
			return 0, errUnknownAsynqQueue
		}
		queues = append(queues, queueFilter)
	} else {
		discovered, err := s.asynqInspector.Queues()
		if err != nil {
			return 0, err
		}
		for _, queue := range discovered {
			if asynqQueueOwnedByServer(s, queue) {
				queues = append(queues, queue)
			}
		}
		sort.Strings(queues)
	}

	total := 0
	for _, queue := range queues {
		var (
			count int
			err   error
		)
		switch sourceState {
		case "retry":
			count, err = s.asynqInspector.RunAllRetryTasks(queue)
		case "archived":
			count, err = s.asynqInspector.RunAllArchivedTasks(queue)
		}
		if errors.Is(err, asynq.ErrQueueNotFound) {
			continue
		}
		if err != nil {
			return total, err
		}
		total += count
	}
	return total, nil
}

func asynqActionPageNumber(value string) int {
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 {
		return 1
	}
	return min(page, asynqTaskMaxPage)
}

func asynqTaskActionRedirectURL(state string, queue string, page int, flashKey string, message string) string {
	values := url.Values{}
	if queue != "" {
		values.Set("queue", queue)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(min(page, asynqTaskMaxPage)))
	}
	if (flashKey == "notice" || flashKey == "error") && message != "" {
		values.Set(flashKey, message)
	}
	target := "/asynq/" + url.PathEscape(state)
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return target
}

func (s *Server) asynqAuthorizedUser(c *echo.Context) (*models.User, string, string, error) {
	user, err := s.requireDevopsWebUser(c)
	if err != nil {
		return nil, "", "", err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionViewDevops) {
		if responseErr := c.HTML(http.StatusForbidden, asynqPageShell("", adminT(locale, "admin.dashboard.not_permitted", "You are not allowed to view the dashboard."), "", locale, theme)); responseErr != nil {
			return nil, locale, theme, responseErr
		}
		return nil, locale, theme, errWebAuthResponseHandled
	}
	return user, locale, theme, nil
}

func (s *Server) sidekiqStats(c *echo.Context) error {
	user, err := s.requireDevopsWebUser(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if !s.userCan(user, rolePermissionViewDevops) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": adminT(locale, "admin.dashboard.not_permitted", "You are not allowed to view the dashboard.")})
	}
	data, collectErr := s.collectAsynqDashboard(locale)
	if collectErr != nil {
		return c.JSON(http.StatusServiceUnavailable, asynqUnavailableSnapshot(locale, collectErr))
	}
	return c.JSON(http.StatusOK, data.Snapshot)
}

func (s *Server) retryAsynqTask(c *echo.Context) error {
	_, locale, _, err := s.asynqAuthorizedUser(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	return s.retryAsynqTaskAuthorized(c, locale)
}

func (s *Server) retryAllAsynqTasks(c *echo.Context) error {
	_, locale, _, err := s.asynqAuthorizedUser(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	return s.retryAllAsynqTasksAuthorized(c, locale)
}

func (s *Server) retryAllAsynqTasksAuthorized(c *echo.Context, locale string) error {
	sourceState := c.FormValue("source_state")
	if _, ok := asynqRetryTaskState(sourceState); !ok {
		return echo.NewHTTPError(http.StatusBadRequest, adminT(locale, "admin.devops.retry_invalid_state", "Only retry and archived tasks can be retried."))
	}
	queue := c.FormValue("queue")
	if queue != "" && !asynqQueueOwnedByServer(s, queue) {
		return echo.NewHTTPError(http.StatusBadRequest, adminT(locale, "admin.devops.unknown_queue", "Unknown queue"))
	}
	count, err := s.runAllAsynqTasksNow(sourceState, queue)
	if err != nil {
		return c.Redirect(http.StatusSeeOther, asynqTaskActionRedirectURL(sourceState, queue, 1, "error", adminT(locale, "admin.devops.retry_all_failed", "The matching tasks could not be retried. Refresh and try again.")))
	}
	message := fmt.Sprintf(adminT(locale, "admin.devops.retry_all_success", "%d tasks were moved to the pending queue."), count)
	return c.Redirect(http.StatusSeeOther, asynqTaskActionRedirectURL(sourceState, queue, 1, "notice", message))
}

func (s *Server) retryAsynqTaskAuthorized(c *echo.Context, locale string) error {
	sourceState := c.FormValue("source_state")
	if _, ok := asynqRetryTaskState(sourceState); !ok {
		return echo.NewHTTPError(http.StatusBadRequest, adminT(locale, "admin.devops.retry_invalid_state", "Only retry and archived tasks can be retried."))
	}
	queue := c.FormValue("queue")
	returnQueue := c.FormValue("return_queue")
	if !asynqQueueOwnedByServer(s, queue) || (returnQueue != "" && !asynqQueueOwnedByServer(s, returnQueue)) {
		return echo.NewHTTPError(http.StatusBadRequest, adminT(locale, "admin.devops.unknown_queue", "Unknown queue"))
	}
	page := asynqActionPageNumber(c.FormValue("return_page"))
	err := s.runAsynqTaskNow(c.Request().Context(), sourceState, queue, c.FormValue("task_id"))
	if err == nil {
		return c.Redirect(http.StatusSeeOther, asynqTaskActionRedirectURL(sourceState, returnQueue, page, "notice", adminT(locale, "admin.devops.retry_success", "The task was moved to the pending queue.")))
	}
	if errors.Is(err, asynq.ErrTaskNotFound) || errors.Is(err, asynq.ErrQueueNotFound) || errors.Is(err, errAsynqTaskStateChanged) {
		return c.Redirect(http.StatusSeeOther, asynqTaskActionRedirectURL(sourceState, returnQueue, page, "error", adminT(locale, "admin.devops.retry_stale", "The task is no longer available in this list. Refresh and check its current state.")))
	}
	return c.Redirect(http.StatusSeeOther, asynqTaskActionRedirectURL(sourceState, returnQueue, page, "error", adminT(locale, "admin.devops.retry_failed", "The task could not be retried. Refresh and try again.")))
}

func (s *Server) sidekiqPage(c *echo.Context) error {
	_, locale, theme, err := s.asynqAuthorizedUser(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	view, ok := asynqViewForPath(c.Request().URL.Path)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	data, collectErr := s.collectAsynqDashboard(locale)
	dashboardAvailable := collectErr == nil
	if collectErr != nil {
		unavailable := asynqUnavailableSnapshot(locale, collectErr)
		data = &asynqDashboardData{Snapshot: unavailable, QueueInfos: map[string]*asynq.QueueInfo{}, ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{}}
	}
	var taskPage *asynqTaskPage
	if dashboardAvailable && (view == "active" || view == "pending" || view == "retry" || view == "scheduled" || view == "archived") {
		page, pageErr := s.asynqTaskPage(data, view, c.QueryParam("queue"), asynqPageNumber(c))
		if pageErr != nil {
			if errors.Is(pageErr, errUnknownAsynqQueue) {
				return echo.NewHTTPError(http.StatusBadRequest, adminT(locale, "admin.devops.unknown_queue", "Unknown queue"))
			}
			data.Snapshot = asynqUnavailableSnapshot(locale, pageErr)
		} else {
			taskPage = &page
		}
	}
	body := asynqPageHTML(data.Snapshot, taskPage, view, locale)
	return c.HTML(http.StatusOK, asynqPageShell(c.QueryParam("notice"), c.QueryParam("error"), body, locale, theme))
}

func asynqPageShell(notice string, errorText string, body string, locale string, theme string) string {
	assets := currentAppAssets()
	if strings.TrimSpace(locale) == "" {
		locale = webDefaultLocaleValue()
	}
	return `<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
  <head>
    ` + buildAppHead("Asynq", theme) + `
  </head>
  <body class="admin theme-` + html.EscapeString(normalizedWebTheme(theme)) + ` no-reduce-motion asynq-page">
    <div class="admin-wrapper">
      <main class="content asynq-page__content" role="main">
        <div class="content__heading"><div class="content__heading__row"><div class="asynq-page__title"><a class="asynq-page__logo" href="/" aria-label="Paon"><img alt="Paon" src="` + html.EscapeString(assets.logoDesktopSVG) + `"></a><h2>Asynq</h2></div></div></div>
        ` + settingsFlashHTML(notice, errorText) + body + `
      </main>
    </div>
    <script src="` + html.EscapeString(assets.publicJS) + `" crossorigin="anonymous" defer></script>
    <script src="` + html.EscapeString(assets.adminJS) + `" crossorigin="anonymous" defer></script>
    <div class="logo-resources" tabindex="-1" aria-hidden="true"></div>
  </body>
</html>`
}

func asynqHTMLAttr(value string) string {
	return html.EscapeString(value)
}

func asynqPollingHTML(locale string, timestamp string, errorText string) string {
	errorHidden := ""
	if errorText == "" {
		errorHidden = ` hidden`
	}
	updatedHidden := ""
	if timestamp == "" {
		updatedHidden = ` hidden`
	}
	return `<div class="asynq-dashboard__toolbar">
  <div class="fields-row">
    <label><input id="asynq_polling_enabled" type="checkbox" checked> ` + asynqHTMLAttr(adminT(locale, "admin.devops.auto_refresh", "Auto refresh")) + `</label>
    <label for="asynq_polling_interval">` + asynqHTMLAttr(adminT(locale, "admin.devops.polling_interval", "Polling interval")) + `</label>
    <input id="asynq_polling_interval" type="range" min="2" max="20" step="1" value="5">
    <output id="asynq_polling_interval_value" for="asynq_polling_interval">5s</output>
  </div>
  <div class="asynq-dashboard__freshness">
    <span data-asynq-last-updated data-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.last_updated", "Last updated")) + `" data-stale-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.stale", "Stale")) + `"` + updatedHidden + `>` + asynqHTMLAttr(timestamp) + `</span>
    <span data-asynq-error role="alert"` + errorHidden + `>` + asynqHTMLAttr(errorText) + `</span>
  </div>
</div>`
}

func asynqTabsHTML(view string, locale string) string {
	tabs := []struct {
		View     string
		Href     string
		Key      string
		Fallback string
	}{
		{"overview", "/asynq", "admin.devops.overview", "Overview"},
		{"active", "/asynq/active", "admin.devops.active", "Active"},
		{"pending", "/asynq/pending", "admin.devops.pending", "Pending"},
		{"queues", "/asynq/queues", "admin.devops.queues", "Queues"},
		{"retry", "/asynq/retry", "admin.devops.retry", "Retry"},
		{"scheduled", "/asynq/scheduled", "admin.devops.scheduled", "Scheduled"},
		{"archived", "/asynq/archived", "admin.devops.archived", "Archived"},
	}
	var body strings.Builder
	body.WriteString(`<nav class="asynq-tabs" aria-label="Asynq">`)
	for _, tab := range tabs {
		current := ""
		if tab.View == view {
			current = ` class="selected" aria-current="page"`
		}
		body.WriteString(`<a href="` + tab.Href + `"` + current + `>` + asynqHTMLAttr(adminT(locale, tab.Key, tab.Fallback)) + `</a>`)
	}
	body.WriteString(`</nav>`)
	return body.String()
}

func asynqSummaryHTML(summary asynqDashboardSummary, available bool, locale string) string {
	items := []struct {
		Key      string
		Value    int
		Locale   string
		Fallback string
	}{
		{"processed_total", summary.ProcessedTotal, "admin.devops.processed_total", "Processed"},
		{"failed_total", summary.FailedTotal, "admin.devops.failed_total", "Failed"},
		{"active", summary.Active, "admin.devops.active", "Active"},
		{"pending", summary.Pending, "admin.devops.pending", "Pending"},
		{"retry", summary.Retry, "admin.devops.retry", "Retry"},
		{"scheduled", summary.Scheduled, "admin.devops.scheduled", "Scheduled"},
		{"archived", summary.Archived, "admin.devops.archived", "Archived"},
	}
	var body strings.Builder
	body.WriteString(`<section data-asynq-summary aria-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.summary", "Asynq summary")) + `">`)
	for _, item := range items {
		value := "—"
		if available {
			value = strconv.Itoa(item.Value)
		}
		body.WriteString(`<div><strong data-asynq-counter="` + item.Key + `">` + value + `</strong>` + asynqHTMLAttr(adminT(locale, item.Locale, item.Fallback)) + `</div>`)
	}
	body.WriteString(`</section>`)
	return body.String()
}

func asynqAlertsHTML(issues []asynqDashboardIssue, locale string) string {
	hidden := ""
	if len(issues) == 0 {
		hidden = ` hidden`
	}
	var body strings.Builder
	body.WriteString(`<section data-asynq-alerts aria-live="polite"` + hidden + `>`)
	for _, issue := range issues {
		body.WriteString(`<div class="asynq-alert asynq-alert--` + asynqHTMLAttr(issue.Severity) + `"><strong>` + asynqHTMLAttr(issue.Label) + `</strong>`)
		if issue.Queue != "" {
			body.WriteString(`<span class="asynq-alert__queue">` + asynqHTMLAttr(adminT(locale, "admin.devops.queue", "Queue")) + `: <code>` + asynqHTMLAttr(issue.Queue) + `</code></span>`)
		}
		if issue.Detail != "" {
			body.WriteString(`<span>` + asynqHTMLAttr(issue.Detail) + `</span>`)
		}
		body.WriteString(`</div>`)
	}
	body.WriteString(`</section>`)
	return body.String()
}

func asynqQueueRowsHTML(queues []asynqQueueView, locale string) string {
	if len(queues) == 0 {
		return `<tr><td class="asynq-table__empty" colspan="9">` + asynqHTMLAttr(adminT(locale, "admin.devops.empty", "No data")) + `</td></tr>`
	}
	var body strings.Builder
	for _, queue := range queues {
		rowClass := ""
		if len(queue.Issues) > 0 {
			rowClass = ` class="asynq-table__row--attention"`
		}
		body.WriteString(`<tr data-queue="` + asynqHTMLAttr(queue.Name) + `"` + rowClass + `><th scope="row"><strong>` + asynqHTMLAttr(queue.DisplayName) + `</strong>`)
		if queue.Name != queue.DisplayName {
			body.WriteString(`<code>` + asynqHTMLAttr(queue.Name) + `</code>`)
		}
		body.WriteString(`</th><td class="asynq-table__number">` + strconv.Itoa(queue.Pending) + `</td><td class="asynq-table__number">` + strconv.Itoa(queue.Active) + `</td><td class="asynq-table__number">` + strconv.Itoa(queue.Retry) + `</td><td class="asynq-table__number">` + strconv.Itoa(queue.Scheduled) + `</td><td class="asynq-table__number">` + strconv.Itoa(queue.Archived) + `</td><td class="asynq-table__number">` + asynqHTMLAttr(queue.Latency) + `</td><td class="asynq-table__number">` + asynqHTMLAttr(queue.Memory) + `</td><td class="asynq-queue__status"><span class="asynq-badge asynq-badge--` + asynqHTMLAttr(queue.Status) + `">` + asynqHTMLAttr(queue.StatusLabel) + `</span><small class="asynq-queue__metadata">` + asynqHTMLAttr(adminT(locale, "admin.devops.consumers", "Consumers")) + `: ` + strconv.Itoa(queue.ActiveConsumers) + ` · ` + asynqHTMLAttr(adminT(locale, "admin.devops.failed_total", "Failed total")) + `: ` + strconv.Itoa(queue.FailedTotal) + `</small>`)
		if len(queue.Issues) > 0 {
			body.WriteString(`<ul class="asynq-queue__issues">`)
			for _, issue := range queue.Issues {
				body.WriteString(`<li class="asynq-queue__issue asynq-queue__issue--` + asynqHTMLAttr(issue.Severity) + `">` + asynqHTMLAttr(issue.Label))
				if issue.Detail != "" {
					body.WriteString(`<span>` + asynqHTMLAttr(issue.Detail) + `</span>`)
				}
				body.WriteString(`</li>`)
			}
			body.WriteString(`</ul>`)
		}
		body.WriteString(`</td></tr>`)
	}
	return body.String()
}

func asynqQueuesHTML(queues []asynqQueueView, locale string) string {
	headers := []string{
		adminT(locale, "admin.devops.queue", "Queue"),
		adminT(locale, "admin.devops.pending", "Pending"),
		adminT(locale, "admin.devops.active", "Active"),
		adminT(locale, "admin.devops.retry", "Retry"),
		adminT(locale, "admin.devops.scheduled", "Scheduled"),
		adminT(locale, "admin.devops.archived", "Archived"),
		adminT(locale, "admin.devops.latency", "Latency"),
		adminT(locale, "admin.devops.memory", "Memory"),
		adminT(locale, "admin.devops.status", "Status"),
	}
	var header strings.Builder
	for _, label := range headers {
		header.WriteString(`<th scope="col">` + asynqHTMLAttr(label) + `</th>`)
	}
	return `<section class="asynq-section"><div class="asynq-section__heading"><h2>` + asynqHTMLAttr(adminT(locale, "admin.devops.queues", "Queues")) + `</h2></div><div class="asynq-table-wrapper table-wrapper"><table class="table"><thead><tr>` + header.String() + `</tr></thead><tbody data-asynq-queue-body>` + asynqQueueRowsHTML(queues, locale) + `</tbody></table></div></section>`
}

func asynqWorkerHTML(worker asynqWorkerView, locale string) string {
	orphan := ""
	if worker.Orphaned {
		orphan = `<span class="asynq-badge asynq-badge--error">` + asynqHTMLAttr(adminT(locale, "admin.devops.orphaned", "Orphaned")) + `</span>`
	}
	metadata := `<span>` + asynqHTMLAttr(adminT(locale, "admin.devops.queue", "Queue")) + `: ` + asynqHTMLAttr(worker.Queue) + `</span>`
	if worker.StartedAt != "" {
		metadata += `<span>` + asynqHTMLAttr(adminT(locale, "admin.devops.started_at", "Started")) + `: <time datetime="` + asynqHTMLAttr(worker.StartedAt) + `">` + asynqHTMLAttr(worker.StartedAt) + `</time></span>`
	}
	if worker.Elapsed != "" {
		metadata += `<span>` + asynqHTMLAttr(adminT(locale, "admin.devops.elapsed", "Elapsed")) + `: ` + asynqHTMLAttr(worker.Elapsed) + `</span>`
	}
	if worker.Deadline != "" {
		metadata += `<span>` + asynqHTMLAttr(adminT(locale, "admin.devops.deadline", "Deadline")) + `: <time datetime="` + asynqHTMLAttr(worker.Deadline) + `">` + asynqHTMLAttr(worker.Deadline) + `</time></span>`
	}
	return `<li class="asynq-worker"><div class="asynq-worker__heading"><strong>` + asynqHTMLAttr(worker.TaskType) + `</strong><code>` + asynqHTMLAttr(worker.TaskID) + `</code>` + orphan + `</div><div class="asynq-worker__metadata">` + metadata + `</div></li>`
}

func asynqServerRowsHTML(servers []asynqServerView, locale string) string {
	if len(servers) == 0 {
		return `<tr><td class="asynq-table__empty" colspan="6">` + asynqHTMLAttr(adminT(locale, "admin.devops.empty", "No data")) + `</td></tr>`
	}
	var body strings.Builder
	for _, server := range servers {
		body.WriteString(`<tr data-server="` + asynqHTMLAttr(server.ID) + `"><th scope="row"><strong>` + asynqHTMLAttr(server.Host) + `</strong><code>` + asynqHTMLAttr(server.ID) + `</code><small>PID ` + strconv.Itoa(server.PID) + `</small>`)
		if server.StartedAt != "" {
			body.WriteString(`<small>` + asynqHTMLAttr(adminT(locale, "admin.devops.started_at", "Started")) + `: <time datetime="` + asynqHTMLAttr(server.StartedAt) + `">` + asynqHTMLAttr(server.StartedAt) + `</time></small>`)
		}
		body.WriteString(`</th><td><span class="asynq-badge asynq-badge--` + asynqHTMLAttr(server.Status) + `">` + asynqHTMLAttr(server.Status) + `</span></td><td class="asynq-table__number">` + strconv.Itoa(server.Concurrency) + `</td><td class="asynq-table__number">` + strconv.Itoa(server.Active) + ` / ` + strconv.Itoa(server.Concurrency) + ` / ` + strconv.FormatFloat(server.Utilization, 'f', 1, 64) + `%</td><td class="asynq-server__queues">`)
		for _, queue := range server.Queues {
			body.WriteString(`<code>` + asynqHTMLAttr(queue) + `</code>`)
		}
		body.WriteString(`</td><td>`)
		if len(server.Workers) == 0 {
			body.WriteString(`—`)
		} else {
			body.WriteString(`<ul class="asynq-workers">`)
			for _, worker := range server.Workers {
				body.WriteString(asynqWorkerHTML(worker, locale))
			}
			body.WriteString(`</ul>`)
		}
		body.WriteString(`</td></tr>`)
	}
	return body.String()
}

func asynqServersHTML(servers []asynqServerView, locale string) string {
	headers := []string{
		adminT(locale, "admin.devops.host", "Host / server"),
		adminT(locale, "admin.devops.status", "Status"),
		adminT(locale, "admin.devops.concurrency", "Concurrency"),
		adminT(locale, "admin.devops.utilization", "Active / capacity"),
		adminT(locale, "admin.devops.queues", "Queues"),
		adminT(locale, "admin.devops.workers", "Running tasks"),
	}
	var header strings.Builder
	for _, label := range headers {
		header.WriteString(`<th scope="col">` + asynqHTMLAttr(label) + `</th>`)
	}
	return `<section class="asynq-section"><div class="asynq-section__heading"><h2>` + asynqHTMLAttr(adminT(locale, "admin.devops.servers", "Servers and running tasks")) + `</h2></div><div class="asynq-table-wrapper table-wrapper"><table class="table"><thead><tr>` + header.String() + `</tr></thead><tbody data-asynq-server-body>` + asynqServerRowsHTML(servers, locale) + `</tbody></table></div></section>`
}

func asynqHistoryRowsHTML(history []asynqHistoryView, locale string) string {
	if len(history) > 7 {
		history = history[len(history)-7:]
	}
	if len(history) == 0 {
		return `<tr><td class="asynq-table__empty" colspan="4">` + asynqHTMLAttr(adminT(locale, "admin.devops.empty", "No data")) + `</td></tr>`
	}
	var body strings.Builder
	for _, row := range history {
		body.WriteString(`<tr><th scope="row">` + asynqHTMLAttr(row.Date) + `</th><td class="asynq-table__number">` + strconv.Itoa(row.Processed) + `</td><td class="asynq-table__number">` + strconv.Itoa(row.Failed) + `</td><td class="asynq-table__number">` + strconv.Itoa(row.Succeeded) + `</td></tr>`)
	}
	return body.String()
}

func asynqHistoryHTML(history []asynqHistoryView, locale string) string {
	return `<section class="asynq-section asynq-history"><div class="asynq-section__heading"><h2>` + asynqHTMLAttr(adminT(locale, "admin.devops.history", "History")) + `</h2><label for="asynq_history_range">` + asynqHTMLAttr(adminT(locale, "admin.devops.range", "Range")) + ` <select id="asynq_history_range"><option value="7" selected>7 ` + asynqHTMLAttr(adminT(locale, "admin.devops.days", "days")) + `</option><option value="30">30 ` + asynqHTMLAttr(adminT(locale, "admin.devops.days", "days")) + `</option><option value="90">90 ` + asynqHTMLAttr(adminT(locale, "admin.devops.days", "days")) + `</option></select></label></div><div class="asynq-table-wrapper table-wrapper"><table class="table"><thead><tr><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.date", "Date")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.processed", "Processed")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.failed", "Failed")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.succeeded", "Succeeded")) + `</th></tr></thead><tbody data-asynq-history-body>` + asynqHistoryRowsHTML(history, locale) + `</tbody></table></div></section>`
}

func asynqTaskTimingHTML(task asynqTaskView, locale string) string {
	var body strings.Builder
	writeTime := func(label string, value time.Time) {
		if value.IsZero() {
			return
		}
		formatted := value.UTC().Format(time.RFC3339)
		body.WriteString(`<span><strong>` + asynqHTMLAttr(label) + `:</strong> <time datetime="` + formatted + `">` + formatted + `</time></span>`)
	}
	writeTime(adminT(locale, "admin.devops.started_at", "Started"), task.StartedAt)
	if task.Elapsed > 0 {
		body.WriteString(`<span><strong>` + asynqHTMLAttr(adminT(locale, "admin.devops.elapsed", "Elapsed")) + `:</strong> ` + asynqHTMLAttr(asynqDuration(task.Elapsed)) + `</span>`)
	}
	writeTime(adminT(locale, "admin.devops.next_process_at", "Next attempt"), task.NextProcessAt)
	writeTime(adminT(locale, "admin.devops.last_failed_at", "Last failed"), task.LastFailedAt)
	writeTime(adminT(locale, "admin.devops.deadline", "Deadline"), task.Deadline)
	if task.WorkerHost != "" {
		body.WriteString(`<span><strong>` + asynqHTMLAttr(adminT(locale, "admin.devops.worker", "Worker")) + `:</strong> ` + asynqHTMLAttr(task.WorkerHost) + ` / PID ` + strconv.Itoa(task.WorkerPID) + `</span>`)
	}
	if body.Len() == 0 {
		return "—"
	}
	return `<div class="asynq-worker__metadata">` + body.String() + `</div>`
}

func asynqTaskActionsAvailable(page *asynqTaskPage) bool {
	if page == nil {
		return false
	}
	_, ok := asynqRetryTaskState(page.State)
	return ok
}

func asynqTaskActionHTML(page *asynqTaskPage, task asynqTaskView, locale string) string {
	if !asynqTaskActionsAvailable(page) {
		return ""
	}
	return `<form action="/asynq/tasks/retry" method="post">` +
		`<input type="hidden" name="source_state" value="` + asynqHTMLAttr(page.State) + `">` +
		`<input type="hidden" name="queue" value="` + asynqHTMLAttr(task.Queue) + `">` +
		`<input type="hidden" name="task_id" value="` + asynqHTMLAttr(task.ID) + `">` +
		`<input type="hidden" name="return_queue" value="` + asynqHTMLAttr(page.Queue) + `">` +
		`<input type="hidden" name="return_page" value="` + strconv.Itoa(page.Page) + `">` +
		`<button type="submit" class="table-action-link" data-confirm="` + asynqHTMLAttr(adminT(locale, "admin.devops.retry_confirm", "Retry this task now? Its retry count will not be reset.")) + `"><i class="fa fa-refresh fa-fw"></i> ` + asynqHTMLAttr(adminT(locale, "admin.devops.retry_now", "Retry now")) + `</button></form>`
}

func asynqTaskDetailsHTML(task asynqTaskView, locale string) string {
	lastError := "—"
	if task.LastError != "" {
		lastError = asynqHTMLAttr(task.LastError)
	}
	rawPayload := "—"
	if task.RawPayload != "" {
		rawPayload = asynqHTMLAttr(task.RawPayload)
	}
	return `<button type="button" class="table-action-link" data-asynq-task-details aria-haspopup="dialog"><i class="fa fa-info-circle fa-fw" aria-hidden="true"></i> ` + asynqHTMLAttr(adminT(locale, "admin.devops.details", "Details")) + `</button>` +
		`<template data-asynq-task-details-template>` +
		`<dl class="asynq-task-modal__metadata">` +
		`<div data-asynq-task-copy-metadata><dt>` + asynqHTMLAttr(adminT(locale, "admin.devops.task_id", "Task ID")) + `</dt><dd><code>` + asynqHTMLAttr(task.ID) + `</code></dd></div>` +
		`<div data-asynq-task-copy-metadata><dt>` + asynqHTMLAttr(adminT(locale, "admin.devops.queue", "Queue")) + `</dt><dd><code>` + asynqHTMLAttr(task.Queue) + `</code></dd></div>` +
		`<div data-asynq-task-copy-metadata><dt>` + asynqHTMLAttr(adminT(locale, "admin.devops.task_type", "Task")) + `</dt><dd><code>` + asynqHTMLAttr(task.Type) + `</code></dd></div>` +
		`</dl>` +
		`<section data-asynq-task-copy-section data-asynq-task-copy-language="text"><h4><span data-asynq-task-copy-label>` + asynqHTMLAttr(adminT(locale, "admin.devops.error", "Last error")) + `</span></h4><pre>` + lastError + `</pre></section>` +
		`<section data-asynq-task-copy-section data-asynq-task-copy-language="json"><h4><span data-asynq-task-copy-label>` + asynqHTMLAttr(adminT(locale, "admin.devops.payload", "Payload")) + `</span> <small>` + strconv.Itoa(task.PayloadBytes) + ` ` + asynqHTMLAttr(adminT(locale, "admin.devops.bytes", "bytes")) + `</small></h4><pre>` + rawPayload + `</pre></section>` +
		`</template>`
}

func asynqTaskDetailsModalHTML(locale string) string {
	closeLabel := asynqHTMLAttr(adminT(locale, "admin.devops.close", "Close"))
	copyLabel := asynqHTMLAttr(adminT(locale, "admin.devops.copy_markdown", "Copy as Markdown"))
	copiedLabel := asynqHTMLAttr(adminT(locale, "admin.devops.copied", "Copied"))
	return `<dialog class="asynq-task-modal" data-asynq-task-modal aria-labelledby="asynq-task-modal-title">` +
		`<div class="asynq-task-modal__header"><h3 class="asynq-task-modal-title" id="asynq-task-modal-title">` + asynqHTMLAttr(adminT(locale, "admin.devops.task_details", "Task details")) + `</h3></div>` +
		`<div class="asynq-task-modal__content" data-asynq-task-modal-content></div>` +
		`<div class="asynq-task-modal__footer"><button type="button" class="button button-secondary" data-asynq-task-copy data-copy-label="` + copyLabel + `" data-copied-label="` + copiedLabel + `"><i class="fa fa-copy fa-fw" aria-hidden="true"></i> <span>` + copyLabel + `</span></button><form method="dialog"><button type="submit" class="button">` + closeLabel + `</button></form></div>` +
		`</dialog>`
}

func asynqTaskRowsHTML(page *asynqTaskPage, locale string) string {
	if page == nil || len(page.Tasks) == 0 {
		columns := 6
		if asynqTaskActionsAvailable(page) {
			columns++
		}
		return `<tr><td class="asynq-table__empty" colspan="` + strconv.Itoa(columns) + `">` + asynqHTMLAttr(adminT(locale, "admin.devops.empty", "No data")) + `</td></tr>`
	}
	var body strings.Builder
	for _, task := range page.Tasks {
		orphan := ""
		if task.IsOrphaned {
			orphan = ` <span class="asynq-badge asynq-badge--error">` + asynqHTMLAttr(adminT(locale, "admin.devops.orphaned", "Orphaned")) + `</span>`
		}
		body.WriteString(`<tr><td><code>` + asynqHTMLAttr(task.ID) + `</code>` + orphan + `</td><td><strong>` + asynqHTMLAttr(task.Queue) + `</strong>`)
		body.WriteString(`</td><td><strong>` + asynqHTMLAttr(task.Type) + `</strong></td><td class="asynq-table__number">` + strconv.Itoa(task.Retried) + ` / ` + strconv.Itoa(task.MaxRetry) + `</td><td>` + asynqTaskTimingHTML(task, locale) + `</td><td class="asynq-task__details">` + asynqTaskDetailsHTML(task, locale) + `</td>`)
		if asynqTaskActionsAvailable(page) {
			body.WriteString(`<td class="asynq-task__actions">` + asynqTaskActionHTML(page, task, locale) + `</td>`)
		}
		body.WriteString(`</tr>`)
	}
	return body.String()
}

func asynqTaskFilterHTML(snapshot asynqDashboardSnapshot, page *asynqTaskPage, locale string) string {
	if page == nil {
		return ""
	}
	var options strings.Builder
	options.WriteString(`<option value="">` + asynqHTMLAttr(adminT(locale, "admin.devops.all_queues", "All queues")) + `</option>`)
	for _, queue := range snapshot.Queues {
		selected := ""
		if page.Queue == queue.Name {
			selected = ` selected`
		}
		options.WriteString(`<option value="` + asynqHTMLAttr(queue.Name) + `"` + selected + `>` + asynqHTMLAttr(queue.DisplayName) + `</option>`)
	}
	filter := `<form action="/asynq/` + asynqHTMLAttr(page.State) + `" method="get"><label for="asynq_queue_filter">` + asynqHTMLAttr(adminT(locale, "admin.devops.filter", "Filter")) + `</label> <select id="asynq_queue_filter" name="queue">` + options.String() + `</select> <button type="submit" class="button">` + asynqHTMLAttr(adminT(locale, "admin.devops.apply", "Apply")) + `</button></form>`
	if !asynqTaskActionsAvailable(page) {
		return `<div class="asynq-task-toolbar">` + filter + `</div>`
	}
	disabled := ""
	if page.Total == 0 {
		disabled = ` disabled`
	}
	retryAll := `<form action="/asynq/tasks/retry_all" method="post">` +
		`<input type="hidden" name="source_state" value="` + asynqHTMLAttr(page.State) + `">` +
		`<input type="hidden" name="queue" value="` + asynqHTMLAttr(page.Queue) + `">` +
		`<button type="submit" class="button" data-confirm="` + asynqHTMLAttr(adminT(locale, "admin.devops.retry_all_confirm", "Retry all tasks matching the current queue filter now? Retry counts will not be reset.")) + `"` + disabled + `><i class="fa fa-refresh fa-fw" aria-hidden="true"></i> ` + asynqHTMLAttr(adminT(locale, "admin.devops.retry_all", "Retry all")) + `</button></form>`
	return `<div class="asynq-task-toolbar">` + filter + retryAll + `</div>`
}

func asynqTaskPaginationHTML(page *asynqTaskPage, locale string) string {
	if page == nil || (!page.HasPrevious && !page.HasNext) {
		return ""
	}
	pageURL := func(number int) string {
		values := url.Values{}
		values.Set("page", strconv.Itoa(number))
		if page.Queue != "" {
			values.Set("queue", page.Queue)
		}
		return "/asynq/" + url.PathEscape(page.State) + "?" + values.Encode()
	}
	var body strings.Builder
	body.WriteString(`<nav class="pagination" aria-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.pagination", "Pagination")) + `">`)
	if page.HasPrevious {
		body.WriteString(`<a class="button" rel="prev" href="` + asynqHTMLAttr(pageURL(page.Page-1)) + `">` + asynqHTMLAttr(adminT(locale, "admin.devops.previous", "Previous")) + `</a>`)
	}
	body.WriteString(`<span>` + fmt.Sprintf(asynqHTMLAttr(adminT(locale, "admin.devops.page_of", "Page %d · %d tasks")), page.Page, page.Total) + `</span>`)
	if page.HasNext {
		body.WriteString(`<a class="button" rel="next" href="` + asynqHTMLAttr(pageURL(page.Page+1)) + `">` + asynqHTMLAttr(adminT(locale, "admin.devops.next", "Next")) + `</a>`)
	}
	body.WriteString(`</nav>`)
	return body.String()
}

func asynqTasksHTML(snapshot asynqDashboardSnapshot, page *asynqTaskPage, view string, locale string) string {
	labels := map[string][2]string{
		"active":    {"admin.devops.active", "Active"},
		"pending":   {"admin.devops.pending", "Pending"},
		"retry":     {"admin.devops.retry", "Retry"},
		"scheduled": {"admin.devops.scheduled", "Scheduled"},
		"archived":  {"admin.devops.archived", "Archived"},
	}
	label := labels[view]
	truncated := ""
	if page != nil && page.Truncated {
		truncated = `<p class="asynq-alert asynq-alert--warning">` + fmt.Sprintf(asynqHTMLAttr(adminT(locale, "admin.devops.task_results_truncated", "The view is limited to %d of %d tasks. Filter by queue to narrow the result.")), page.ResultLimit, page.Total) + `</p>`
	}
	actionsHeader := ""
	if asynqTaskActionsAvailable(page) {
		actionsHeader = `<th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.actions", "Actions")) + `</th>`
	}
	return `<section class="asynq-section"><div class="asynq-section__heading"><h2>` + asynqHTMLAttr(adminT(locale, label[0], label[1])) + `</h2>` + asynqTaskFilterHTML(snapshot, page, locale) + `</div>` + truncated + `<div class="asynq-table-wrapper table-wrapper"><table class="table asynq-task-table"><thead><tr><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.task_id", "Task ID")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.queue", "Queue")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.task_type", "Task")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.retries", "Retries")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.timing", "Timing / worker")) + `</th><th scope="col">` + asynqHTMLAttr(adminT(locale, "admin.devops.details", "Details")) + `</th>` + actionsHeader + `</tr></thead><tbody>` + asynqTaskRowsHTML(page, locale) + `</tbody></table></div>` + asynqTaskPaginationHTML(page, locale) + asynqTaskDetailsModalHTML(locale) + `</section>`
}

func asynqPageHTML(snapshot asynqDashboardSnapshot, taskPage *asynqTaskPage, view string, locale string) string {
	reloadOnPoll := "false"
	if view != "overview" && view != "queues" {
		reloadOnPoll = "true"
	}
	attrs := ` class="asynq-dashboard" data-asynq-dashboard data-stats-url="/asynq/stats" data-poll-reload="` + reloadOnPoll + `" data-refresh-error="` + asynqHTMLAttr(adminT(locale, "admin.devops.refresh_error", "Could not refresh Asynq data.")) + `" data-stale-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.stale", "Stale")) + `" data-last-updated-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.last_updated", "Last updated")) + `" data-empty-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.empty", "No data")) + `" data-queue-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.queue", "Queue")) + `" data-consumers-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.consumers", "Consumers")) + `" data-failed-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.failed_total", "Failed total")) + `" data-started-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.started_at", "Started")) + `" data-elapsed-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.elapsed", "Elapsed")) + `" data-deadline-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.deadline", "Deadline")) + `" data-orphaned-label="` + asynqHTMLAttr(adminT(locale, "admin.devops.orphaned", "Orphaned")) + `" aria-busy="false"`
	var body strings.Builder
	body.WriteString(`<div` + attrs + `>`)
	body.WriteString(asynqTabsHTML(view, locale))
	body.WriteString(asynqPollingHTML(locale, snapshot.Timestamp, snapshot.Error))
	body.WriteString(asynqSummaryHTML(snapshot.Summary, snapshot.Available, locale))
	body.WriteString(asynqAlertsHTML(snapshot.Issues, locale))
	if !snapshot.Available {
		if view != "overview" && view != "queues" {
			body.WriteString(`</div>`)
			return body.String()
		}
		body.WriteString(`<div data-asynq-recovery-content hidden>`)
	}
	switch view {
	case "overview":
		body.WriteString(asynqQueuesHTML(snapshot.Queues, locale))
		body.WriteString(asynqServersHTML(snapshot.Servers, locale))
		body.WriteString(asynqHistoryHTML(snapshot.History, locale))
	case "queues":
		body.WriteString(asynqQueuesHTML(snapshot.Queues, locale))
	default:
		body.WriteString(asynqTasksHTML(snapshot, taskPage, view, locale))
	}
	if !snapshot.Available {
		body.WriteString(`</div>`)
	}
	body.WriteString(`</div>`)
	return body.String()
}
