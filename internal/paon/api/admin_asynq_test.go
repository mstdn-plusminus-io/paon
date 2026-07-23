package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/redis/go-redis/v9"
)

type fakeAsynqInspectorClient struct {
	queues         []string
	queueInfos     map[string]*asynq.QueueInfo
	history        map[string][]*asynq.DailyStats
	servers        []*asynq.ServerInfo
	tasks          map[string]map[string][]*asynq.TaskInfo
	queueErr       error
	queueInfoErr   error
	historyErr     error
	serversErr     error
	listErr        map[string]error
	queueInfoCalls map[string]int
	listCalls      map[string]int
	listRequests   map[string][]fakeAsynqListRequest
	runAllCounts   map[string]int
	runAllErrs     map[string]error
	runAllCalls    []fakeAsynqRunAllRequest
	closeCalls     int
}

type fakeAsynqListRequest struct {
	Page int
	Size int
}

type fakeAsynqTaskRequest struct {
	Queue       string
	TaskID      string
	SourceState string
}

type fakeAsynqRunAllRequest struct {
	State string
	Queue string
}

type fakeAsynqTaskRetryer struct {
	errs  map[string]error
	calls []fakeAsynqTaskRequest
}

func (f *fakeAsynqTaskRetryer) Close() error {
	return nil
}

func (f *fakeAsynqInspectorClient) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeAsynqInspectorClient) Queues() ([]string, error) {
	return append([]string(nil), f.queues...), f.queueErr
}

func (f *fakeAsynqInspectorClient) GetQueueInfo(queue string) (*asynq.QueueInfo, error) {
	if f.queueInfoCalls == nil {
		f.queueInfoCalls = make(map[string]int)
	}
	f.queueInfoCalls[queue]++
	if f.queueInfoErr != nil {
		return nil, f.queueInfoErr
	}
	info, ok := f.queueInfos[queue]
	if !ok {
		return nil, asynq.ErrQueueNotFound
	}
	return info, nil
}

func (f *fakeAsynqInspectorClient) History(queue string, days int) ([]*asynq.DailyStats, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return append([]*asynq.DailyStats(nil), f.history[queue]...), nil
}

func (f *fakeAsynqInspectorClient) Servers() ([]*asynq.ServerInfo, error) {
	return append([]*asynq.ServerInfo(nil), f.servers...), f.serversErr
}

func fakeAsynqListRequestFromOptions(opts []asynq.ListOption) fakeAsynqListRequest {
	request := fakeAsynqListRequest{Page: 1, Size: 30}
	for _, option := range opts {
		value := reflect.ValueOf(option)
		if !value.IsValid() || value.Kind() != reflect.Int {
			continue
		}
		switch reflect.TypeOf(option).Name() {
		case "pageNumOpt":
			request.Page = int(value.Int())
		case "pageSizeOpt":
			request.Size = int(value.Int())
		}
	}
	if request.Page < 1 {
		request.Page = 1
	}
	if request.Size < 0 {
		request.Size = 0
	}
	return request
}

func (f *fakeAsynqInspectorClient) list(state string, queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	if f.listCalls == nil {
		f.listCalls = make(map[string]int)
	}
	key := state + "\x00" + queue
	f.listCalls[key]++
	if f.listRequests == nil {
		f.listRequests = make(map[string][]fakeAsynqListRequest)
	}
	request := fakeAsynqListRequestFromOptions(opts)
	f.listRequests[key] = append(f.listRequests[key], request)
	if err := f.listErr[state]; err != nil {
		return nil, err
	}
	tasks := f.tasks[state][queue]
	start := (request.Page - 1) * request.Size
	if request.Size == 0 || start >= len(tasks) {
		return []*asynq.TaskInfo{}, nil
	}
	end := min(start+request.Size, len(tasks))
	return append([]*asynq.TaskInfo(nil), tasks[start:end]...), nil
}

func (f *fakeAsynqInspectorClient) ListPendingTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("pending", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListActiveTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("active", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListRetryTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("retry", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListScheduledTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("scheduled", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListArchivedTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("archived", queue, opts...)
}

func (f *fakeAsynqInspectorClient) runAll(state string, queue string) (int, error) {
	f.runAllCalls = append(f.runAllCalls, fakeAsynqRunAllRequest{State: state, Queue: queue})
	key := state + "\x00" + queue
	return f.runAllCounts[key], f.runAllErrs[key]
}

func (f *fakeAsynqInspectorClient) RunAllRetryTasks(queue string) (int, error) {
	return f.runAll("retry", queue)
}

func (f *fakeAsynqInspectorClient) RunAllArchivedTasks(queue string) (int, error) {
	return f.runAll("archived", queue)
}

func (f *fakeAsynqTaskRetryer) RetryTask(_ context.Context, queue string, taskID string, sourceState string) error {
	request := fakeAsynqTaskRequest{Queue: queue, TaskID: taskID, SourceState: sourceState}
	f.calls = append(f.calls, request)
	return f.errs[asynqTaskKey(queue, taskID)]
}

func asynqIssueCodesForQueue(issues []asynqDashboardIssue, queue string) []string {
	var out []string
	for _, issue := range issues {
		if issue.Queue == queue {
			out = append(out, issue.Code)
		}
	}
	return out
}

func TestCollectAsynqDashboardAggregatesAndOrdersOperationalState(t *testing.T) {
	now := time.Now().UTC()
	defaultQueue := "tenant:default"
	pullQueue := "tenant:pull"
	zetaQueue := "tenant:zeta"
	inspector := &fakeAsynqInspectorClient{
		queues: []string{zetaQueue, "foreign:ignored", pullQueue, defaultQueue},
		queueInfos: map[string]*asynq.QueueInfo{
			defaultQueue: {
				Queue: defaultQueue, ProcessedTotal: 100, FailedTotal: 5,
				Pending: 2, Active: 1, Retry: 3, Scheduled: 4, Archived: 5,
				Latency: 90 * time.Second, MemoryUsage: 4096, Paused: true,
			},
			pullQueue: {
				Queue: pullQueue, ProcessedTotal: 20, FailedTotal: 1, Pending: 7,
			},
			zetaQueue: {Queue: zetaQueue},
		},
		history: map[string][]*asynq.DailyStats{
			defaultQueue: {
				{Queue: defaultQueue, Date: now.AddDate(0, 0, -1), Processed: 4},
				{Queue: defaultQueue, Date: now, Processed: 10, Failed: 2},
			},
			pullQueue: {{Queue: pullQueue, Date: now, Processed: 5, Failed: 1}},
		},
		servers: []*asynq.ServerInfo{
			{
				ID: "server-z", Host: "z-worker", PID: 20, Status: "active", Concurrency: 1,
				Queues: map[string]int{defaultQueue: 1},
				ActiveWorkers: []*asynq.WorkerInfo{{
					TaskID: "deadline-task", TaskType: "deadline", Queue: defaultQueue,
					Started: now.Add(-2 * time.Minute), Deadline: now.Add(-time.Minute),
				}},
			},
			{
				ID: "server-a", Host: "a-worker", PID: 10, Status: "active", Concurrency: 4,
				Queues: map[string]int{"tenant:ingress": 4}, ActiveWorkers: []*asynq.WorkerInfo{},
			},
			{ID: "foreign", Host: "foreign-worker", Status: "active", Queues: map[string]int{"foreign:ignored": 1}},
		},
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"active": {
				defaultQueue: {{ID: "orphan-task", Queue: defaultQueue, Type: "orphan", State: asynq.TaskStateActive, IsOrphaned: true}},
			},
		},
		listErr: make(map[string]error),
	}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqInspector: inspector}

	data, err := server.collectAsynqDashboard("en")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := data.Snapshot
	if !snapshot.Available || snapshot.Error != "" {
		t.Fatalf("availability = %v error = %q", snapshot.Available, snapshot.Error)
	}
	wantSummary := asynqDashboardSummary{ProcessedTotal: 120, FailedTotal: 6, Active: 1, Pending: 9, Retry: 3, Scheduled: 4, Archived: 5}
	if snapshot.Summary != wantSummary {
		t.Fatalf("summary = %#v, want %#v", snapshot.Summary, wantSummary)
	}

	var queueNames []string
	for _, queue := range snapshot.Queues {
		queueNames = append(queueNames, queue.Name)
	}
	wantQueues := []string{"tenant:default", "tenant:ingress", "tenant:mailers", "tenant:pull", "tenant:push", "tenant:zeta"}
	if !reflect.DeepEqual(queueNames, wantQueues) {
		t.Fatalf("queue order = %#v, want %#v", queueNames, wantQueues)
	}
	if got := snapshot.Queues[len(snapshot.Queues)-1].DisplayName; got != "zeta" {
		t.Fatalf("discovered unknown queue display name = %q", got)
	}
	if got := snapshot.Queues[0]; got.ActiveConsumers != 1 || !got.ConsumerAvailable || got.ConsumerCapacity != 1 || got.FailedTotal != 5 {
		t.Fatalf("default queue consumer/failure metrics = %#v", got)
	}
	if len(snapshot.Servers) != 2 || snapshot.Servers[0].Host != "a-worker" || snapshot.Servers[1].Host != "z-worker" {
		t.Fatalf("server order/filtering = %#v", snapshot.Servers)
	}
	if snapshot.Servers[1].Utilization != 100 || snapshot.Servers[1].Workers[0].Elapsed == "" {
		t.Fatalf("server utilization/worker elapsed = %#v", snapshot.Servers[1])
	}

	wantDefaultIssues := []string{"archived", "deadline_exceeded", "orphaned", "paused", "retry", "saturated"}
	if got := asynqIssueCodesForQueue(snapshot.Issues, "default"); !reflect.DeepEqual(got, wantDefaultIssues) {
		t.Fatalf("default issues = %#v, want %#v", got, wantDefaultIssues)
	}
	if got := asynqIssueCodesForQueue(snapshot.Issues, "pull"); !reflect.DeepEqual(got, []string{"no_consumer"}) {
		t.Fatalf("pull issues = %#v", got)
	}
	if len(snapshot.History) != asynqHistoryDays {
		t.Fatalf("history length = %d, want %d", len(snapshot.History), asynqHistoryDays)
	}
	var currentDay *asynqHistoryView
	for i := range snapshot.History {
		if snapshot.History[i].Date == now.Format("2006-01-02") {
			currentDay = &snapshot.History[i]
			break
		}
	}
	if currentDay == nil || currentDay.Processed != 15 || currentDay.Failed != 3 || currentDay.Succeeded != 12 {
		t.Fatalf("aggregated current-day history = %#v", currentDay)
	}
	if inspector.queueInfoCalls["foreign:ignored"] != 0 {
		t.Fatal("foreign namespace queue was inspected")
	}
}

func TestCollectAsynqDashboardWithoutNamespaceIgnoresUnconfiguredQueues(t *testing.T) {
	inspector := &fakeAsynqInspectorClient{
		queues: []string{"default", "another-app"},
		queueInfos: map[string]*asynq.QueueInfo{
			"default":     {Queue: "default", Pending: 2},
			"another-app": {Queue: "another-app", Pending: 999},
		},
		history: map[string][]*asynq.DailyStats{},
		servers: []*asynq.ServerInfo{},
		tasks:   map[string]map[string][]*asynq.TaskInfo{},
		listErr: map[string]error{},
	}
	server := &Server{asynqInspector: inspector}
	data, err := server.collectAsynqDashboard("en")
	if err != nil {
		t.Fatal(err)
	}
	if data.Snapshot.Summary.Pending != 2 {
		t.Fatalf("pending = %d, want only Paon queue count 2", data.Snapshot.Summary.Pending)
	}
	if inspector.queueInfoCalls["another-app"] != 0 {
		t.Fatal("an unconfigured queue was inspected without an ownership namespace")
	}
}

func TestCollectAsynqDashboardDoesNotReportSaturationWhenQueueHasNoActiveTasks(t *testing.T) {
	pushQueue := "tenant:push"
	pullQueue := "tenant:pull"
	inspector := &fakeAsynqInspectorClient{
		queueInfos: map[string]*asynq.QueueInfo{
			pushQueue: {Queue: pushQueue, Pending: 26, Active: 0},
			pullQueue: {Queue: pullQueue, Active: 1},
		},
		history: map[string][]*asynq.DailyStats{},
		servers: []*asynq.ServerInfo{{
			ID:          "shared-worker",
			Status:      "active",
			Concurrency: 1,
			Queues:      map[string]int{pushQueue: 1, pullQueue: 1},
			ActiveWorkers: []*asynq.WorkerInfo{{
				TaskID: "pull-task",
				Queue:  pullQueue,
			}},
		}},
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"active": {
				pullQueue: {{ID: "pull-task", Queue: pullQueue, State: asynq.TaskStateActive}},
			},
		},
		listErr: map[string]error{},
	}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqInspector: inspector}

	data, err := server.collectAsynqDashboard("ja")
	if err != nil {
		t.Fatal(err)
	}
	if got := asynqIssueCodesForQueue(data.Snapshot.Issues, "push"); len(got) != 0 {
		t.Fatalf("push issues = %#v, want no saturation issue while active is zero", got)
	}
}

func TestAsynqAlertsHTMLShowsLogicalQueueAndEscapesIt(t *testing.T) {
	html := asynqAlertsHTML([]asynqDashboardIssue{{
		Severity: "critical",
		Label:    "アーカイブ済みタスクを確認してください",
		Detail:   "1件のタスクがアーカイブされています。",
		Queue:    `push"><script>alert(1)</script>`,
	}}, "ja")

	for _, want := range []string{
		`アーカイブ済みタスクを確認してください`,
		`class="asynq-alert__queue">キュー: <code>push&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;</code>`,
		`1件のタスクがアーカイブされています。`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("alert HTML missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("alert HTML contains executable queue name: %s", html)
	}
}

func TestCollectAsynqDashboardPropagatesInspectorFailuresAndStatsUses503(t *testing.T) {
	redisFailure := errors.New("dial redis://secret@redis.internal:6379: refused")
	cases := []struct {
		name      string
		inspector *fakeAsynqInspectorClient
	}{
		{"queues", &fakeAsynqInspectorClient{queueErr: redisFailure}},
		{"queue info", &fakeAsynqInspectorClient{queues: []string{"default"}, queueInfos: map[string]*asynq.QueueInfo{}, queueInfoErr: redisFailure}},
		{"servers", &fakeAsynqInspectorClient{serversErr: redisFailure}},
		{"active tasks", &fakeAsynqInspectorClient{
			queues: []string{"default"}, queueInfos: map[string]*asynq.QueueInfo{"default": {Queue: "default", Active: 1}},
			tasks: map[string]map[string][]*asynq.TaskInfo{}, listErr: map[string]error{"active": redisFailure},
		}},
		{"history", &fakeAsynqInspectorClient{
			queues: []string{"default"}, queueInfos: map[string]*asynq.QueueInfo{"default": {Queue: "default"}}, historyErr: redisFailure,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{asynqInspector: tc.inspector}
			if _, err := server.collectAsynqDashboard("en"); err == nil {
				t.Fatal("collectAsynqDashboard succeeded despite inspector failure")
			}
			snapshot := asynqUnavailableSnapshot("en", redisFailure)
			if snapshot.Available || snapshot.Error == "" || strings.Contains(snapshot.Error, "redis.internal") || len(snapshot.Queues) != 0 {
				t.Fatalf("unsafe unavailable snapshot = %#v", snapshot)
			}
		})
	}

	src, err := os.ReadFile("admin_asynq.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "sidekiqStats", "http.StatusServiceUnavailable") {
		t.Fatal("sidekiqStats must return HTTP 503 when Asynq inspection fails")
	}
}

func TestAsynqTaskDetailsShowCompleteRawPayloadAndErrorWithHTMLEscaping(t *testing.T) {
	payload := []byte(`{"token":"ADMIN-TOKEN","body":"</pre><script>alert('payload')</script>","large":"` + strings.Repeat("x", 70*1024) + `"}`)
	lastError := `delivery failed: privateKey=ERROR-SECRET </pre><script>alert('error')</script> ` + strings.Repeat("e", 2_000)
	inspector := &fakeAsynqInspectorClient{
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"pending": {"default": {{ID: "raw-task", Queue: "default", Type: "raw", Payload: payload, LastErr: lastError}}},
		},
		listErr: map[string]error{},
	}
	server := &Server{asynqInspector: inspector}
	data := &asynqDashboardData{
		QueueInfos:  map[string]*asynq.QueueInfo{"default": {Queue: "default", Pending: 1}},
		ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
	}
	page, err := server.asynqTaskPage(data, "pending", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(page.Tasks))
	}
	task := page.Tasks[0]
	if task.RawPayload != string(payload) {
		t.Fatalf("raw payload changed: got %d bytes, want %d", len(task.RawPayload), len(payload))
	}
	if task.LastError != lastError {
		t.Fatalf("last error changed: got %d bytes, want %d", len(task.LastError), len(lastError))
	}

	rows := asynqTaskRowsHTML(&page, "en")
	for _, want := range []string{"ADMIN-TOKEN", "ERROR-SECRET", `data-asynq-task-details-template`, "&lt;/pre&gt;&lt;script&gt;alert(&#39;payload&#39;)&lt;/script&gt;", "&lt;/pre&gt;&lt;script&gt;alert(&#39;error&#39;)&lt;/script&gt;"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("task HTML missing %q", want)
		}
	}
	for _, unwanted := range []string{"<script>", "[REDACTED]", "payload omitted", "error detail redacted"} {
		if strings.Contains(rows, unwanted) {
			t.Fatalf("task HTML unexpectedly contains %q", unwanted)
		}
	}
}

func TestRunAllAsynqTasksNowUsesOfficialBulkOperationsWithinOwnedQueues(t *testing.T) {
	inspector := &fakeAsynqInspectorClient{
		queues: []string{"foreign:default", "tenant:push", "tenant:pull"},
		runAllCounts: map[string]int{
			"retry\x00tenant:pull": 7,
			"retry\x00tenant:push": 5,
		},
		runAllErrs: map[string]error{},
	}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqInspector: inspector}

	count, err := server.runAllAsynqTasksNow("retry", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("bulk retry count = %d, want 12", count)
	}
	wantCalls := []fakeAsynqRunAllRequest{
		{State: "retry", Queue: "tenant:pull"},
		{State: "retry", Queue: "tenant:push"},
	}
	if !reflect.DeepEqual(inspector.runAllCalls, wantCalls) {
		t.Fatalf("bulk retry calls = %#v, want %#v", inspector.runAllCalls, wantCalls)
	}

	inspector.runAllCalls = nil
	inspector.runAllCounts["archived\x00tenant:push"] = 3
	count, err = server.runAllAsynqTasksNow("archived", "tenant:push")
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 || !reflect.DeepEqual(inspector.runAllCalls, []fakeAsynqRunAllRequest{{State: "archived", Queue: "tenant:push"}}) {
		t.Fatalf("filtered archived retry count=%d calls=%#v", count, inspector.runAllCalls)
	}
	if _, err := server.runAllAsynqTasksNow("retry", "foreign:default"); !errors.Is(err, errUnknownAsynqQueue) {
		t.Fatalf("foreign queue error = %v, want errUnknownAsynqQueue", err)
	}
}

func TestRunAsynqTaskNowAllowsOnlyRetryAndArchivedTasks(t *testing.T) {
	for _, sourceState := range []string{"retry", "archived"} {
		t.Run(sourceState, func(t *testing.T) {
			request := fakeAsynqTaskRequest{Queue: "tenant:pull", TaskID: sourceState + "-task", SourceState: sourceState}
			retryer := &fakeAsynqTaskRetryer{}
			server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqTaskRetryer: retryer}
			if err := server.runAsynqTaskNow(context.Background(), sourceState, request.Queue, request.TaskID); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retryer.calls, []fakeAsynqTaskRequest{request}) {
				t.Fatalf("retry calls = %#v", retryer.calls)
			}
		})
	}

	retryer := &fakeAsynqTaskRetryer{}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant"}, asynqTaskRetryer: retryer}
	if err := server.runAsynqTaskNow(context.Background(), "scheduled", "tenant:pull", "task"); !errors.Is(err, errUnsupportedAsynqRetryState) {
		t.Fatalf("scheduled source error = %v", err)
	}
	if len(retryer.calls) != 0 {
		t.Fatalf("unsupported source reached retryer: %#v", retryer.calls)
	}
}

func TestRunAsynqTaskNowEnforcesQueueOwnershipAndPropagatesRetryErrors(t *testing.T) {
	retryer := &fakeAsynqTaskRetryer{}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqTaskRetryer: retryer}
	if err := server.runAsynqTaskNow(context.Background(), "retry", "foreign:pull", "task"); !errors.Is(err, errUnknownAsynqQueue) {
		t.Fatalf("foreign queue error = %v", err)
	}
	if len(retryer.calls) != 0 {
		t.Fatalf("foreign queue reached retryer: %#v", retryer.calls)
	}
	if err := server.runAsynqTaskNow(context.Background(), "retry", "tenant:pull", " "); !errors.Is(err, asynq.ErrTaskNotFound) {
		t.Fatalf("blank task ID error = %v", err)
	}

	request := fakeAsynqTaskRequest{Queue: "tenant:pull", TaskID: "run-failure", SourceState: "retry"}
	runFailure := errors.New("redis unavailable")
	retryer.errs = map[string]error{asynqTaskKey(request.Queue, request.TaskID): runFailure}
	if err := server.runAsynqTaskNow(context.Background(), "retry", request.Queue, request.TaskID); !errors.Is(err, runFailure) {
		t.Fatalf("retry error = %v", err)
	}
	if !reflect.DeepEqual(retryer.calls, []fakeAsynqTaskRequest{request}) {
		t.Fatalf("retry calls = %#v", retryer.calls)
	}

	plainRetryer := &fakeAsynqTaskRetryer{}
	plainServer := &Server{asynqTaskRetryer: plainRetryer}
	if err := plainServer.runAsynqTaskNow(context.Background(), "retry", "default", "configured-task"); err != nil {
		t.Fatalf("configured queue without namespace failed: %v", err)
	}
	if err := plainServer.runAsynqTaskNow(context.Background(), "retry", "another-app", "task"); !errors.Is(err, errUnknownAsynqQueue) {
		t.Fatalf("unconfigured queue without namespace error = %v", err)
	}
	if len(plainRetryer.calls) != 1 {
		t.Fatalf("unconfigured queue reached retryer: %#v", plainRetryer.calls)
	}
}

func TestRedisAsynqTaskRetryerUsesAtomicExpectedStateTransition(t *testing.T) {
	var gotKeys []string
	var gotArgs []interface{}
	var hasDeadline bool
	retryer := redisAsynqTaskRetryer{
		run: func(ctx context.Context, keys []string, args ...interface{}) (int64, error) {
			_, hasDeadline = ctx.Deadline()
			gotKeys = append([]string(nil), keys...)
			gotArgs = append([]interface{}(nil), args...)
			return 1, nil
		},
	}
	if err := retryer.RetryTask(context.Background(), "tenant:pull", "task-123", "archived"); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{
		"asynq:{tenant:pull}:t:task-123",
		"asynq:{tenant:pull}:archived",
		"asynq:{tenant:pull}:pending",
	}
	wantArgs := []interface{}{"task-123", "archived"}
	if !hasDeadline || !reflect.DeepEqual(gotKeys, wantKeys) || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Redis script deadline=%v keys=%#v args=%#v, want keys=%#v args=%#v", hasDeadline, gotKeys, gotArgs, wantKeys, wantArgs)
	}
	stateCheck := strings.Index(retryAsynqTaskFromStateScript, `state ~= ARGV[2]`)
	removeFromSource := strings.Index(retryAsynqTaskFromStateScript, `ZREM`)
	moveToPending := strings.Index(retryAsynqTaskFromStateScript, `LPUSH`)
	if stateCheck < 0 || removeFromSource <= stateCheck || moveToPending <= removeFromSource {
		t.Fatalf("retry script does not atomically check state before moving the task: %s", retryAsynqTaskFromStateScript)
	}
}

func TestRedisAsynqTaskRetryerMapsAtomicTransitionResults(t *testing.T) {
	cases := []struct {
		name string
		code int64
		err  error
		want error
	}{
		{name: "missing task", code: -1, want: asynq.ErrTaskNotFound},
		{name: "state changed", code: -2, want: errAsynqTaskStateChanged},
		{name: "source set changed", code: -3, want: errAsynqTaskStateChanged},
		{name: "redis failure", err: errors.New("redis unavailable")},
		{name: "unexpected code", code: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryer := redisAsynqTaskRetryer{run: func(context.Context, []string, ...interface{}) (int64, error) {
				return tc.code, tc.err
			}}
			err := retryer.RetryTask(context.Background(), "default", "task", "retry")
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want command error %v", err, tc.err)
			}
			if tc.want == nil && tc.err == nil && err == nil {
				t.Fatal("unexpected Redis response was accepted")
			}
		})
	}
}

func TestNewRedisAsynqTaskRetryerUsesSidekiqRedisConnection(t *testing.T) {
	retryer, err := newRedisAsynqTaskRetryer(config.Config{
		RedisURL:        "redis://main.example.test:6379/0",
		SidekiqRedisURL: "rediss://worker:secret@sidekiq.example.test:6380/4",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer retryer.Close()
	client, ok := retryer.client.(*redis.Client)
	if !ok {
		t.Fatalf("retry Redis client type = %T", retryer.client)
	}
	options := client.Options()
	if options.Addr != "sidekiq.example.test:6380" || options.Username != "worker" || options.Password != "secret" || options.DB != 4 || options.TLSConfig == nil || options.MaxRetries != 0 {
		t.Fatalf("retry Redis options = %#v", options)
	}
}

func TestAsynqTaskRetryActionsRenderOnlyForRetryAndArchived(t *testing.T) {
	task := asynqTaskView{ID: `task"><script>alert(1)</script>`, Queue: "tenant:pull", DisplayQueue: "pull", Type: "delivery"}
	for _, state := range []string{"retry", "archived"} {
		page := &asynqTaskPage{State: state, Queue: "tenant:pull", Page: 3, Tasks: []asynqTaskView{task}}
		rows := asynqTaskRowsHTML(page, "ja")
		for _, want := range []string{
			`action="/asynq/tasks/retry" method="post"`,
			`name="source_state" value="` + state + `"`,
			`name="queue" value="tenant:pull"`,
			`name="return_queue" value="tenant:pull"`,
			`name="return_page" value="3"`,
			`今すぐ再試行`,
			`再試行回数はリセットされません`,
			`task&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`,
			`data-asynq-task-copy-metadata`,
			`data-asynq-task-copy-section`,
		} {
			if !strings.Contains(rows, want) {
				t.Fatalf("%s task rows missing %q: %s", state, want, rows)
			}
		}
		if strings.Contains(rows, `<script>`) {
			t.Fatalf("%s task rows contain executable task ID: %s", state, rows)
		}
		if strings.Contains(rows, `<small>`+state+`</small>`) {
			t.Fatalf("%s task rows redundantly display the page state in the task cell: %s", state, rows)
		}
		if !htmlNeedsBrowserCSRF(`<html><body>` + rows + `</body></html>`) {
			t.Fatalf("%s retry form was not detected as CSRF-protected", state)
		}
		withCSRF := injectBrowserCSRF(`<html><body>`+rows+`</body></html>`, "csrf-token")
		if !strings.Contains(withCSRF, `name="authenticity_token" value="csrf-token"`) {
			t.Fatalf("%s retry form did not receive a CSRF token: %s", state, withCSRF)
		}
	}

	for _, state := range []string{"active", "pending", "scheduled"} {
		rows := asynqTaskRowsHTML(&asynqTaskPage{State: state, Page: 1, Tasks: []asynqTaskView{task}}, "en")
		if strings.Contains(rows, `/asynq/tasks/retry`) || strings.Contains(rows, `Retry now`) {
			t.Fatalf("%s task unexpectedly has a retry action: %s", state, rows)
		}
		if strings.Contains(rows, `<small>`+state+`</small>`) {
			t.Fatalf("%s task rows redundantly display the page state in the task cell: %s", state, rows)
		}
	}
	if rows := asynqTaskRowsHTML(&asynqTaskPage{State: "retry"}, "en"); !strings.Contains(rows, `colspan="7"`) {
		t.Fatalf("empty retry rows have the wrong colspan: %s", rows)
	}
	if rows := asynqTaskRowsHTML(&asynqTaskPage{State: "pending"}, "en"); !strings.Contains(rows, `colspan="6"`) {
		t.Fatalf("empty pending rows have the wrong colspan: %s", rows)
	}
	retryPage := &asynqTaskPage{State: "retry", Page: 1, Total: 1, Tasks: []asynqTaskView{task}}
	pageHTML := asynqTasksHTML(asynqDashboardSnapshot{Queues: []asynqQueueView{{Name: "tenant:pull", DisplayName: "pull"}}}, retryPage, "retry", "en")
	for _, want := range []string{
		`<th scope="col">Details</th>`,
		`<th scope="col">Actions</th>`,
		`action="/asynq/tasks/retry_all" method="post"`,
		`name="source_state" value="retry"`,
		`Retry all`,
		`data-asynq-task-modal`,
		`data-asynq-task-copy`,
		`Copy as Markdown`,
	} {
		if !strings.Contains(pageHTML, want) {
			t.Fatalf("retry page is missing %q: %s", want, pageHTML)
		}
	}
	if strings.Contains(pageHTML, `class="icon-button"`) || strings.Contains(pageHTML, `fa-times`) {
		t.Fatalf("task details modal retained the duplicate icon close button: %s", pageHTML)
	}
	if modal := asynqTaskDetailsModalHTML("ja"); !strings.Contains(modal, `Markdownとしてコピー`) || !strings.Contains(modal, `コピーしました`) {
		t.Fatalf("task details modal copy labels are not localized: %s", modal)
	}
	if strings.Contains(pageHTML, `<th scope="col">Last error</th>`) || strings.Contains(pageHTML, `<th scope="col">Payload</th>`) {
		t.Fatalf("retry page retained wide error or payload columns: %s", pageHTML)
	}
	if !htmlNeedsBrowserCSRF(`<html><body>` + pageHTML + `</body></html>`) {
		t.Fatalf("retry-all form was not detected as CSRF-protected: %s", pageHTML)
	}
	filterEnd := strings.Index(pageHTML, `</form><form action="/asynq/tasks/retry_all"`)
	if filterEnd < 0 {
		t.Fatalf("retry-all button is not rendered to the right of the filter: %s", pageHTML)
	}
}

func TestAsynqTaskActionRedirectURLPreservesListContext(t *testing.T) {
	got := asynqTaskActionRedirectURL("retry", "tenant:pull", 3, "notice", "queued now")
	want := "/asynq/retry?notice=queued+now&page=3&queue=tenant%3Apull"
	if got != want {
		t.Fatalf("redirect URL = %q, want %q", got, want)
	}
	if unsafe := asynqTaskActionRedirectURL("https://evil.example", "", 1, "", ""); !strings.HasPrefix(unsafe, "/asynq/") {
		t.Fatalf("redirect escaped the Asynq path: %q", unsafe)
	}
}

func TestRetryAsynqTaskAuthorizedParsesActionAndRedirectsToListContext(t *testing.T) {
	for _, sourceState := range []string{"retry", "archived"} {
		t.Run(sourceState, func(t *testing.T) {
			retryer := &fakeAsynqTaskRetryer{}
			server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqTaskRetryer: retryer}
			form := url.Values{
				"source_state": {sourceState},
				"queue":        {"tenant:pull"},
				"task_id":      {sourceState + "-task"},
				"return_queue": {"tenant:pull"},
				"return_page":  {"3"},
			}
			request := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			ctx := echo.NewContext(request, recorder, echo.New())
			if err := server.retryAsynqTaskAuthorized(ctx, "en"); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			location := recorder.Header().Get("Location")
			if !strings.HasPrefix(location, "/asynq/"+sourceState+"?") || !strings.Contains(location, "notice=") || !strings.Contains(location, "page=3") || !strings.Contains(location, "queue=tenant%3Apull") {
				t.Fatalf("Location = %q", location)
			}
			wantCall := fakeAsynqTaskRequest{Queue: "tenant:pull", TaskID: sourceState + "-task", SourceState: sourceState}
			if !reflect.DeepEqual(retryer.calls, []fakeAsynqTaskRequest{wantCall}) {
				t.Fatalf("retry calls = %#v", retryer.calls)
			}
		})
	}

	staleRetryer := &fakeAsynqTaskRetryer{errs: map[string]error{asynqTaskKey("default", "stale-task"): errAsynqTaskStateChanged}}
	staleServer := &Server{asynqTaskRetryer: staleRetryer}
	form := url.Values{"source_state": {"retry"}, "queue": {"default"}, "task_id": {"stale-task"}}
	request := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	if err := staleServer.retryAsynqTaskAuthorized(echo.NewContext(request, recorder, echo.New()), "en"); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "error=") {
		t.Fatalf("stale redirect status=%d Location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestRetryAllAsynqTasksAuthorizedPreservesFilterAndReportsCount(t *testing.T) {
	inspector := &fakeAsynqInspectorClient{
		runAllCounts: map[string]int{"archived\x00tenant:pull": 717},
		runAllErrs:   map[string]error{},
	}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqInspector: inspector}
	form := url.Values{"source_state": {"archived"}, "queue": {"tenant:pull"}}
	request := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry_all", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	if err := server.retryAllAsynqTasksAuthorized(echo.NewContext(request, recorder, echo.New()), "ja"); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/asynq/archived?") || !strings.Contains(location, "queue=tenant%3Apull") || !strings.Contains(location, "717") {
		t.Fatalf("Location = %q", location)
	}
}

func TestAsynqRetryMutationRequiresBrowserCSRFBeforeCallingRetryer(t *testing.T) {
	server := newBrowserSecurityTestServer()
	retryer := &fakeAsynqTaskRetryer{}
	server.asynqTaskRetryer = retryer
	e := echo.New()
	e.Use(server.browserSecurityMiddleware)
	e.GET("/asynq/retry", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<html><body><form method="post" action="/asynq/tasks/retry"></form></body></html>`)
	})
	e.POST("/asynq/tasks/retry", func(c *echo.Context) error {
		return server.retryAsynqTaskAuthorized(c, "en")
	})

	authCookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated"}
	getRequest := httptest.NewRequest(http.MethodGet, "/asynq/retry", nil)
	getRequest.AddCookie(authCookie)
	getRecorder := httptest.NewRecorder()
	e.ServeHTTP(getRecorder, getRequest)
	browserCookie := browserSessionCookieFromRecorder(t, getRecorder)
	state, err := server.openBrowserSession(browserCookie.Value)
	if err != nil {
		t.Fatal(err)
	}

	actionForm := url.Values{"source_state": {"retry"}, "queue": {"default"}, "task_id": {"task"}}
	missingRequest := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry", strings.NewReader(actionForm.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingRequest.AddCookie(authCookie)
	missingRequest.AddCookie(browserCookie)
	missingRecorder := httptest.NewRecorder()
	e.ServeHTTP(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing CSRF status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}
	if len(retryer.calls) != 0 {
		t.Fatalf("missing CSRF reached retryer: %#v", retryer.calls)
	}

	actionForm.Set("authenticity_token", state.CSRFToken)
	validRequest := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry", strings.NewReader(actionForm.Encode()))
	validRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validRequest.AddCookie(authCookie)
	validRequest.AddCookie(browserCookie)
	validRecorder := httptest.NewRecorder()
	e.ServeHTTP(validRecorder, validRequest)
	if validRecorder.Code != http.StatusSeeOther {
		t.Fatalf("valid CSRF status=%d body=%s", validRecorder.Code, validRecorder.Body.String())
	}
	if len(retryer.calls) != 1 {
		t.Fatalf("valid CSRF retry calls = %#v", retryer.calls)
	}
}

func TestAsynqRetryAllMutationRequiresBrowserCSRF(t *testing.T) {
	server := newBrowserSecurityTestServer()
	inspector := &fakeAsynqInspectorClient{
		runAllCounts: map[string]int{"retry\x00default": 2},
		runAllErrs:   map[string]error{},
	}
	server.asynqInspector = inspector
	e := echo.New()
	e.Use(server.browserSecurityMiddleware)
	e.GET("/asynq/retry", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<html><body><form method="post" action="/asynq/tasks/retry_all"></form></body></html>`)
	})
	e.POST("/asynq/tasks/retry_all", func(c *echo.Context) error {
		return server.retryAllAsynqTasksAuthorized(c, "en")
	})

	authCookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated"}
	getRequest := httptest.NewRequest(http.MethodGet, "/asynq/retry", nil)
	getRequest.AddCookie(authCookie)
	getRecorder := httptest.NewRecorder()
	e.ServeHTTP(getRecorder, getRequest)
	browserCookie := browserSessionCookieFromRecorder(t, getRecorder)
	state, err := server.openBrowserSession(browserCookie.Value)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"source_state": {"retry"}, "queue": {"default"}}
	missingRequest := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry_all", strings.NewReader(form.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingRequest.AddCookie(authCookie)
	missingRequest.AddCookie(browserCookie)
	missingRecorder := httptest.NewRecorder()
	e.ServeHTTP(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusUnprocessableEntity || len(inspector.runAllCalls) != 0 {
		t.Fatalf("missing CSRF status=%d calls=%#v", missingRecorder.Code, inspector.runAllCalls)
	}

	form.Set("authenticity_token", state.CSRFToken)
	validRequest := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry_all", strings.NewReader(form.Encode()))
	validRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validRequest.AddCookie(authCookie)
	validRequest.AddCookie(browserCookie)
	validRecorder := httptest.NewRecorder()
	e.ServeHTTP(validRecorder, validRequest)
	if validRecorder.Code != http.StatusSeeOther || !reflect.DeepEqual(inspector.runAllCalls, []fakeAsynqRunAllRequest{{State: "retry", Queue: "default"}}) {
		t.Fatalf("valid CSRF status=%d calls=%#v", validRecorder.Code, inspector.runAllCalls)
	}
}

func makeAsynqTaskInfosInReverseLexicalOrder(queue string, prefix string, count int) []*asynq.TaskInfo {
	tasks := make([]*asynq.TaskInfo, 0, count)
	for i := 0; i < count; i++ {
		tasks = append(tasks, &asynq.TaskInfo{ID: fmt.Sprintf("%s%03d", prefix, count-1-i), Queue: queue, Type: "test"})
	}
	return tasks
}

func TestAsynqTaskPagePaginatesGloballyAcrossQueues(t *testing.T) {
	inspector := &fakeAsynqInspectorClient{
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"pending": {
				"a": makeAsynqTaskInfosInReverseLexicalOrder("a", "a", 60),
				"b": makeAsynqTaskInfosInReverseLexicalOrder("b", "b", 60),
			},
		},
		listErr: make(map[string]error),
	}
	server := &Server{asynqInspector: inspector}
	data := &asynqDashboardData{
		QueueInfos:  map[string]*asynq.QueueInfo{"b": {Queue: "b", Pending: 60}, "a": {Queue: "a", Pending: 60}},
		ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
	}
	first, err := server.asynqTaskPage(data, "pending", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 120 || len(first.Tasks) != 50 || first.Tasks[0].ID != "a059" || first.Tasks[49].ID != "a010" || first.HasPrevious || !first.HasNext {
		t.Fatalf("first global page = %#v", first)
	}
	for _, key := range []string{"pending\x00a", "pending\x00b"} {
		if got := inspector.listRequests[key]; !reflect.DeepEqual(got, []fakeAsynqListRequest{{Page: 1, Size: 50}}) {
			t.Fatalf("first-page Inspector request %q = %#v", key, got)
		}
	}
	second, err := server.asynqTaskPage(data, "pending", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 120 || len(second.Tasks) != 50 || second.Tasks[0].ID != "a009" || second.Tasks[9].ID != "a000" || second.Tasks[10].ID != "b059" || second.Tasks[49].ID != "b020" || !second.HasPrevious || !second.HasNext {
		t.Fatalf("second global page = %#v", second)
	}
	firstIDs := make(map[string]struct{}, len(first.Tasks))
	for _, task := range first.Tasks {
		firstIDs[task.Queue+"\x00"+task.ID] = struct{}{}
	}
	for _, task := range second.Tasks {
		if _, ok := firstIDs[task.Queue+"\x00"+task.ID]; ok {
			t.Fatalf("task %s/%s appeared on both pages", task.Queue, task.ID)
		}
	}
	for _, key := range []string{"pending\x00a", "pending\x00b"} {
		want := []fakeAsynqListRequest{{Page: 1, Size: 50}, {Page: 1, Size: 60}}
		if got := inspector.listRequests[key]; !reflect.DeepEqual(got, want) {
			t.Fatalf("global Inspector requests %q = %#v, want %#v", key, got, want)
		}
	}
}

func TestAsynqArchivedTaskPageUsesGlobalNewestOrdering(t *testing.T) {
	base := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	archived := map[string][]*asynq.TaskInfo{"a": {}, "b": {}}
	for position := 0; position < 75; position++ {
		queue := "a"
		if position%5 == 0 {
			queue = "b"
		}
		// Inspector returns archived tasks oldest-to-newest. IDs deliberately do
		// not follow time order so this catches accidental lexical sorting.
		archived[queue] = append(archived[queue], &asynq.TaskInfo{
			ID:           fmt.Sprintf("id-%03d", (position*37)%101),
			Queue:        queue,
			LastFailedAt: base.Add(time.Duration(position) * time.Minute),
		})
	}
	inspector := &fakeAsynqInspectorClient{
		tasks:   map[string]map[string][]*asynq.TaskInfo{"archived": archived},
		listErr: make(map[string]error),
	}
	server := &Server{asynqInspector: inspector}
	data := &asynqDashboardData{
		QueueInfos:  map[string]*asynq.QueueInfo{"a": {Queue: "a", Archived: 60}, "b": {Queue: "b", Archived: 15}},
		ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
	}
	first, err := server.asynqTaskPage(data, "archived", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 75 || len(first.Tasks) != 50 || first.Tasks[0].ID != "id-011" || first.Tasks[49].ID != "id-016" || !first.HasNext {
		t.Fatalf("newest archived page = %#v", first.Tasks)
	}
	for i := 1; i < len(first.Tasks); i++ {
		if first.Tasks[i].LastFailedAt.After(first.Tasks[i-1].LastFailedAt) {
			t.Fatalf("archived tasks are not newest-first at %d: %#v", i, first.Tasks[i-1:i+1])
		}
	}
	second, err := server.asynqTaskPage(data, "archived", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Tasks) != 25 || second.Tasks[0].ID != "id-080" || second.Tasks[24].ID != "id-000" || !second.HasPrevious || second.HasNext {
		t.Fatalf("second archived page = %#v", second.Tasks)
	}
	firstIDs := make(map[string]struct{}, len(first.Tasks))
	for _, task := range first.Tasks {
		firstIDs[task.Queue+"\x00"+task.ID] = struct{}{}
	}
	for _, task := range second.Tasks {
		if _, ok := firstIDs[task.Queue+"\x00"+task.ID]; ok {
			t.Fatalf("archived task %s/%s appeared on both pages", task.Queue, task.ID)
		}
	}
	if got, want := inspector.listRequests["archived\x00a"], []fakeAsynqListRequest{{Page: 1, Size: 50}, {Page: 2, Size: 50}, {Page: 1, Size: 60}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archived tail-window requests for queue a = %#v, want %#v", got, want)
	}
	if got, want := inspector.listRequests["archived\x00b"], []fakeAsynqListRequest{{Page: 1, Size: 15}, {Page: 1, Size: 15}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archived tail-window requests for queue b = %#v, want %#v", got, want)
	}
}

func TestAsynqTaskPageSupportsEveryReadOnlyStateAndRejectsUnknownQueue(t *testing.T) {
	states := []string{"active", "pending", "retry", "scheduled", "archived"}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			info := &asynq.QueueInfo{Queue: "tenant:custom"}
			switch state {
			case "active":
				info.Active = 1
			case "pending":
				info.Pending = 1
			case "retry":
				info.Retry = 1
			case "scheduled":
				info.Scheduled = 1
			case "archived":
				info.Archived = 1
			}
			inspector := &fakeAsynqInspectorClient{
				tasks: map[string]map[string][]*asynq.TaskInfo{
					state: {"tenant:custom": {{ID: state + "-task", Queue: "tenant:custom", Type: "test"}}},
				},
				listErr: make(map[string]error),
			}
			server := &Server{cfg: config.Config{RedisNamespace: "tenant"}, asynqInspector: inspector}
			data := &asynqDashboardData{
				QueueInfos: map[string]*asynq.QueueInfo{"tenant:custom": info}, ActiveTasks: map[string][]*asynq.TaskInfo{},
				WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
			}
			page, err := server.asynqTaskPage(data, state, "custom", 1)
			if err != nil {
				t.Fatal(err)
			}
			if page.State != state || page.Queue != "tenant:custom" || len(page.Tasks) != 1 || page.Tasks[0].ID != state+"-task" {
				t.Fatalf("%s page = %#v", state, page)
			}
			if inspector.listCalls[state+"\x00tenant:custom"] != 1 {
				t.Fatalf("%s list calls = %#v", state, inspector.listCalls)
			}

			callKey := state + "\x00tenant:custom"
			before := inspector.listCalls[callKey]
			if _, err := server.asynqTaskPage(data, state, "missing", 1); !errors.Is(err, errUnknownAsynqQueue) {
				t.Fatalf("unknown queue error = %v", err)
			}
			if inspector.listCalls[callKey] != before {
				t.Fatal("unknown queue reached Inspector")
			}
		})
	}
}
